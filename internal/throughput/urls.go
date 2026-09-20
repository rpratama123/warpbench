package throughput

import (
	"fmt"
	"net/url"
)

// withQueryParam returns rawURL carrying key=value, replacing any existing value
// for that key rather than appending a duplicate.
//
// Several protocols express payload size as a query parameter (LibreSpeed's
// ckSize, Cloudflare's bytes), and the stored server-list URL deliberately omits
// it because the right value depends on the run.
func withQueryParam(rawURL, key, value string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("empty URL")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing %q: %w", rawURL, err)
	}

	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}
