#!/usr/bin/env bash
# check-servers.sh — validate that every candidate speed-test endpoint is alive.
#
# Probe strategy: HEAD first (cheap, gives Content-Length). If HEAD does not
# return 200/206, retry with a ranged GET capped by --max-filesize so a server
# that ignores Range can never make us pull a 100 MB payload.
#
# The curl exit code is reported, because the interesting failure modes differ:
#   0  ok                      6  DNS failure           7  connect failure
#  28  timeout                63  --max-filesize exceeded (server ignored Range,
#                                 so the body was large => endpoint is FINE)
#
# Usage: check-servers.sh [endpoints.tsv]
#   endpoints.tsv: <id>\t<kind>\t<url>   kind in {ping,download,upload,trace}
# Output: TSV  id, kind, status, method, exitcode, bytes, total, ip, note

set -u

MAXBYTES=2000000
TIMEOUT=25

# One temp file per process invocation *and* per call: $$ is NOT unique inside
# background subshells of the same shell, so parallel probes used to clobber a
# single shared path.
HDR=$(mktemp) || exit 1
trap 'rm -f "$HDR"' EXIT

# get_header <file> <header-name>
get_header() {
  awk -v h="$2" 'BEGIN{IGNORECASE=1} index(tolower($0),tolower(h)":")==1 {sub(/\r$/,""); sub(/^[^:]*:[ \t]*/,""); print; exit}' "$1"
}

probe() {
  local id="$1" kind="$2" url="$3"
  local method="" code="" ip="" xc="" total="" bytes=""

  # iperf3 has no HTTP surface, so liveness is a TCP connect to the advertised
  # port. The caller passes "host:port" as the target.
  if [ "$kind" = "iperf3" ]; then
    local ihost="${url%%:*}" iport="${url##*:}" iip
    iip=$(getent ahostsv4 "$ihost" 2>/dev/null | awk '{print $1; exit}')
    if timeout 8 bash -c "exec 3<>/dev/tcp/$ihost/$iport" 2>/dev/null; then
      printf '%s\tiperf3\topen\tTCP\texit=0\tgot=0\ttotal=?\t%s\t\n' "$id" "${iip:-none}"
    else
      printf '%s\tiperf3\tclosed\tTCP\texit=1\tgot=0\ttotal=?\t%s\tTCP_FAIL\n' "$id" "${iip:-none}"
    fi
    return
  fi

  : > "$HDR"

  if [ "$kind" = "trace" ]; then
    # The trace endpoint is tiny and returns 404 for HEAD, so use a plain GET.
    method="GET"
    code=$(curl -sS -L -m "$TIMEOUT" -o /dev/null -D "$HDR" \
           -w '%{http_code}\t%{remote_ip}\t%{size_download}' "$url" 2>/dev/null)
    xc=$?
    ip=$(printf '%s' "$code" | cut -f2)
    bytes=$(printf '%s' "$code" | cut -f3)
    code=$(printf '%s' "$code" | cut -f1)
  else
    method="HEAD"
    code=$(curl -sS -I -L -m "$TIMEOUT" -o /dev/null -D "$HDR" \
           -w '%{http_code}\t%{remote_ip}\t%{size_download}' "$url" 2>/dev/null)
    xc=$?
    ip=$(printf '%s' "$code" | cut -f2)
    bytes=$(printf '%s' "$code" | cut -f3)
    code=$(printf '%s' "$code" | cut -f1)
    total=$(get_header "$HDR" content-length)
  fi

  if [ "$kind" != "trace" ] && [ "$code" != "200" ] && [ "$code" != "206" ]; then
    method="GET(range)"
    : > "$HDR"
    code=$(curl -sS -L -m "$TIMEOUT" -r 0-1023 --max-filesize "$MAXBYTES" \
           -o /dev/null -D "$HDR" \
           -w '%{http_code}\t%{remote_ip}\t%{size_download}' "$url" 2>/dev/null)
    xc=$?
    ip=$(printf '%s' "$code" | cut -f2)
    bytes=$(printf '%s' "$code" | cut -f3)
    code=$(printf '%s' "$code" | cut -f1)
    total=$(get_header "$HDR" content-range)
    [ -n "$total" ] && total=${total##*/}
  fi

  local note=""
  case "$xc" in
    0)  ;;
    63) note="RANGE_IGNORED_BUT_ALIVE" ;;
    6)  note="DNS_FAIL" ;;
    7)  note="CONNECT_FAIL" ;;
    28) note="TIMEOUT" ;;
    35|60) note="TLS_FAIL" ;;
    *)  note="curl_exit_$xc" ;;
  esac

  if [ "$kind" = "upload" ]; then
    # Finite payload: /dev/zero is an infinite stream and would make curl upload
    # until the timeout expired.
    local payload up
    payload=$(mktemp)
    head -c 1024 /dev/zero > "$payload"
    up=$(curl -sS -m "$TIMEOUT" -X POST --data-binary "@$payload" \
         -H 'Content-Type: application/octet-stream' -o /dev/null \
         -w '%{http_code}\t%{size_upload}' "$url" 2>/dev/null)
    local uxc=$?
    rm -f "$payload"
    code=$(printf '%s' "$up" | cut -f1)
    local upbytes
    upbytes=$(printf '%s' "$up" | cut -f2)
    note="POST_${upbytes}B"
    xc=$uxc
  fi

  printf '%s\t%s\t%s\t%s\texit=%s\tgot=%s\ttotal=%s\t%s\t%s\n' \
    "$id" "$kind" "${code:-000}" "$method" "$xc" "${bytes:-0}" \
    "${total:-?}" "${ip:-none}" "$note"
}

resolve() {
  local host="$1"
  local v4 v6
  v4=$(getent ahostsv4 "$host" 2>/dev/null | awk '{print $1; exit}')
  v6=$(getent ahostsv6 "$host" 2>/dev/null | awk '{print $1; exit}')
  if [ -z "$v4" ] && [ -z "$v6" ]; then
    printf 'FAIL\t-\t-'
  else
    printf 'ok\t%s\t%s' "${v4:-none}" "${v6:-none}"
  fi
}

if [ "${1:-}" = "--dns" ]; then
  while IFS= read -r host; do
    [ -z "$host" ] && continue
    printf '%s\t%s\n' "$host" "$(resolve "$host")"
  done
  exit 0
fi

INPUT="${1:-/dev/stdin}"
while IFS=$'\t' read -r id kind url; do
  [ -z "${id:-}" ] && continue
  case "$id" in \#*) continue ;; esac
  probe "$id" "$kind" "$url"
done < "$INPUT"
