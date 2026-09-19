<#
.SYNOPSIS
    Download and run the warpbench binary on Windows.

.DESCRIPTION
    This script does exactly four things:
      1. detect the CPU architecture
      2. download the matching release binary and SHA256SUMS into a per-user cache
      3. verify the SHA-256 checksum, and refuse to run on a mismatch
      4. run the binary, passing your arguments through unchanged

    It never requires Administrator, never writes outside the cache directory,
    and contacts only github.com and the objects.githubusercontent.com host that
    GitHub redirects release downloads to.

    Optional environment overrides:
      WARPBENCH_VERSION     pin a release tag, e.g. v0.1.0      (default: latest)
      WARPBENCH_CACHE_DIR   cache location (default $env:LOCALAPPDATA\warpbench)
      WARPBENCH_BASE_URL    release base URL (for testing or self-hosting)

    Works on Windows PowerShell 5.1 and PowerShell 7+.

.EXAMPLE
    irm https://raw.githubusercontent.com/rpratama123/warpbench/main/warpbench.ps1 | iex

.EXAMPLE
    .\warpbench.ps1 --quick --no-color
#>
[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $WarpbenchArgs
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# Invoke-WebRequest's progress bar makes downloads dramatically slower in 5.1.
$ProgressPreference = 'SilentlyContinue'

# PowerShell 7.3+ can turn a native command's stderr into a terminating error
# when ErrorActionPreference is 'Stop'. warpbench reports progress on stderr, so
# that behaviour must be off or every run would look like a crash.
if (Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue) {
    $PSNativeCommandUseErrorActionPreference = $false
}

$Repo = 'rpratama123/warpbench'
$Prog = 'warpbench'

# When this script is piped into Invoke-Expression ('irm ... | iex') there is no
# script file and $PSCommandPath is empty. That matters: 'exit' from an iex'd
# block tears down the caller's entire PowerShell session, which is a rude way
# to report a failed download. From a file we exit so the status reaches CI.
$RunAsFile = [bool]$PSCommandPath

function Write-Message {
    param([Parameter(Mandatory = $true)][string] $Message)
    [Console]::Error.WriteLine("${Prog}: $Message")
}

# Aborts the run. The message is written exactly once, by the handler at the
# bottom of this file, so callers must not print it themselves.
function Write-Fatal {
    param([Parameter(Mandatory = $true)][string] $Message)
    throw [System.InvalidOperationException]::new($Message)
}

# ASSET NAMING CONTRACT: goreleaser publishes bare binaries named
#   warpbench_<os>_<arch>[.exe]   os in {linux,darwin,windows}, arch in {amd64,arm64}
# with no version in the filename, so /releases/latest/download/<asset> resolves
# without knowing the tag. Keep .goreleaser.yaml in sync with this function.
function Get-Architecture {
    $arch = $null
    try {
        # Reports the OS architecture, which is what a native binary must match.
        # Present on .NET 4.7.1+ (so Windows PowerShell 5.1 on Windows 10/11) and
        # on PowerShell 7; the env var is the fallback for anything older.
        $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    }
    catch {
        $arch = $null
    }
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }

    switch ($arch) {
        'X64' { return 'amd64' }
        'AMD64' { return 'amd64' }
        'Arm64' { return 'arm64' }
        'ARM64' { return 'arm64' }
        default {
            Write-Fatal "unsupported architecture '$arch'; warpbench ships amd64 and arm64 only"
        }
    }
}

function Get-RemoteFile {
    param(
        [Parameter(Mandatory = $true)][string] $Uri,
        [Parameter(Mandatory = $true)][string] $Destination
    )

    $params = @{
        Uri         = $Uri
        OutFile     = $Destination
        ErrorAction = 'Stop'
        TimeoutSec  = 60
    }
    # Windows PowerShell 5.1 otherwise depends on the Internet Explorer parsing
    # engine, which is unavailable on Server Core and fails on some hardened hosts.
    if ($PSVersionTable.PSVersion.Major -lt 6) { $params['UseBasicParsing'] = $true }

    Invoke-WebRequest @params
}

# Returns the expected lowercase hex digest for $Asset, or $null if absent.
function Get-ExpectedHash {
    param(
        [Parameter(Mandatory = $true)][string] $SumsPath,
        [Parameter(Mandatory = $true)][string] $Asset
    )

    $pattern = '^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($Asset) + '\s*$'
    foreach ($line in (Get-Content -LiteralPath $SumsPath)) {
        if ($line -match $pattern) { return $Matches[1].ToLowerInvariant() }
    }
    return $null
}

function Get-FileDigest {
    param([Parameter(Mandatory = $true)][string] $Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Invoke-Warpbench {
    param(
        [string[]] $ForwardedArgs
    )

    # Windows PowerShell 5.1 on older builds defaults below TLS 1.2, which fails
    # the GitHub download with an opaque error. Add it without removing existing
    # protocols.
    if ($PSVersionTable.PSVersion.Major -lt 6) {
        $current = [Net.ServicePointManager]::SecurityProtocol
        [Net.ServicePointManager]::SecurityProtocol = $current -bor [Net.SecurityProtocolType]::Tls12
    }

    $arch = Get-Architecture
    $asset = "${Prog}_windows_${arch}.exe"

    $cacheRoot = $env:WARPBENCH_CACHE_DIR
    if (-not $cacheRoot) {
        if (-not $env:LOCALAPPDATA) {
            Write-Fatal 'LOCALAPPDATA is not set and WARPBENCH_CACHE_DIR is empty'
        }
        $cacheRoot = Join-Path $env:LOCALAPPDATA $Prog
    }

    $tag = 'latest'
    if ($env:WARPBENCH_VERSION) { $tag = $env:WARPBENCH_VERSION }

    $base = 'https://github.com/' + $Repo + '/releases'
    if ($env:WARPBENCH_BASE_URL) { $base = $env:WARPBENCH_BASE_URL }

    if ($tag -eq 'latest') { $urlBase = "$base/latest/download" } else { $urlBase = "$base/download/$tag" }

    $destDir = Join-Path $cacheRoot $tag
    $binary = Join-Path $destDir $asset
    $sums = Join-Path $destDir 'SHA256SUMS'

    New-Item -ItemType Directory -Force -Path $destDir | Out-Null

    $cached = $false
    if ((Test-Path -LiteralPath $binary) -and (Test-Path -LiteralPath $sums)) {
        $want = Get-ExpectedHash -SumsPath $sums -Asset $asset
        if ($want -and ((Get-FileDigest -Path $binary) -eq $want)) {
            $cached = $true
            Write-Message "using cached $binary"
        }
    }

    if (-not $cached) {
        Write-Message "platform:   $asset"
        Write-Message "release:    $urlBase"

        Write-Message 'downloading SHA256SUMS'
        $sumsPart = "$sums.part"
        try {
            Get-RemoteFile -Uri "$urlBase/SHA256SUMS" -Destination $sumsPart
        }
        catch {
            Write-Fatal "no release found at $urlBase (has a release been published yet?)"
        }
        Move-Item -Force -LiteralPath $sumsPart -Destination $sums

        Write-Message "downloading $asset"
        $binaryPart = "$binary.part"
        try {
            Get-RemoteFile -Uri "$urlBase/$asset" -Destination $binaryPart
        }
        catch {
            Write-Fatal "download failed: $urlBase/$asset"
        }
        Move-Item -Force -LiteralPath $binaryPart -Destination $binary

        $want = Get-ExpectedHash -SumsPath $sums -Asset $asset
        if (-not $want) {
            Remove-Item -Force -LiteralPath $binary, $sums -ErrorAction SilentlyContinue
            Write-Fatal "no checksum entry for $asset in SHA256SUMS; refusing to run it"
        }

        $got = Get-FileDigest -Path $binary
        if ($got -ne $want) {
            Remove-Item -Force -LiteralPath $binary, $sums -ErrorAction SilentlyContinue
            Write-Message "checksum mismatch for $asset"
            Write-Message "  expected $want"
            Write-Message "  actual   $got"
            Write-Fatal 'refusing to run a binary that failed verification'
        }
        Write-Message 'checksum ok'
    }

    # Match warpbench.sh: force non-interactive output when there is no console,
    # rather than letting the TUI draw into a redirect.
    $exeArgs = @()
    if ($ForwardedArgs) { $exeArgs = @($ForwardedArgs) }
    if ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) {
        Write-Message 'no interactive console; running non-interactively (--no-tty)'
        $exeArgs = @('--no-tty') + $exeArgs
    }

    # StrictMode makes reading $LASTEXITCODE a terminating error when no native
    # command has run yet, which would mask the real launch failure. Seed it.
    $global:LASTEXITCODE = 0
    $code = 0
    try {
        & $binary @exeArgs
        $code = $LASTEXITCODE
    }
    catch {
        # A freshly downloaded unsigned binary is sometimes held by antivirus or
        # SmartScreen for a few seconds. Retry once before reporting failure.
        Write-Message "launch failed ($($_.Exception.Message)); retrying once after a short pause"
        Start-Sleep -Seconds 3
        & $binary @exeArgs
        $code = $LASTEXITCODE
    }

    if ($RunAsFile) { exit $code }
    $global:LASTEXITCODE = $code
}

try {
    Invoke-Warpbench -ForwardedArgs $WarpbenchArgs
}
catch {
    Write-Message $_.Exception.Message
    if ($RunAsFile) { exit 1 }
    $global:LASTEXITCODE = 1
}
