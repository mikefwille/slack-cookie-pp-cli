// Hand-authored extension: the d-cookie -> xoxc-token bootstrap exchange.
//
// With only the long-lived `d` cookie, a GET of the workspace web host returns
// HTML whose embedded boot config carries the per-workspace xoxc api_token
// (the slackattack pattern). This is a best-effort convenience for `auth
// login`; when the page shape changes or the cookie is rotated, the caller
// falls back to `auth set-token` with the token supplied directly.
//
// This uses a hand-built raw HTTP GET on purpose: the response is HTML/boot
// JSON, not the JSON-body shape the generated client.Get assumes, and the
// generated client would also force an Authorization header we do not have yet.

package slackclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"slack-cookie-pp-cli/internal/cliutil"
)

// xoxcTokenRe matches the workspace xoxc token embedded in the boot config,
// whether rendered as "api_token":"xoxc-…" or "token":"xoxc-…". The token
// charset is the base62+dash Slack uses for xoxc credentials.
var xoxcTokenRe = regexp.MustCompile(`xoxc-[0-9A-Za-z-]+`)

// ExtractXoxcToken pulls the first xoxc- token out of a workspace bootstrap
// payload (HTML or JSON). Pure and fixture-testable. Returns a typed error
// when no token is present so the caller can give an actionable message.
func ExtractXoxcToken(body string) (string, error) {
	if m := xoxcTokenRe.FindString(body); m != "" {
		return m, nil
	}
	return "", fmt.Errorf("no xoxc token found in workspace bootstrap payload")
}

// MintXoxcFromCookie GETs the workspace web host with the d (+optional d-s)
// cookie and extracts the embedded xoxc token. The HTTP client and timeout are
// supplied by the caller so the command layer controls the deadline.
func MintXoxcFromCookie(ctx context.Context, hc *http.Client, workspaceURL, dCookie, dsCookie string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, workspaceURL, nil)
	if err != nil {
		return "", fmt.Errorf("building bootstrap request: %w", err)
	}
	req.Header.Set("Cookie", cliutil.CookieHeader(dCookie, dsCookie))
	// Slack serves the boot config only to browser-shaped requests.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching workspace bootstrap: %w", err)
	}
	defer resp.Body.Close()
	// Cap the read: the boot config sits near the top of the document, and an
	// unbounded read of a multi-MB page wastes memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("reading workspace bootstrap: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("workspace bootstrap returned HTTP %d (cookie may be expired or rotated)", resp.StatusCode)
	}
	return ExtractXoxcToken(string(body))
}
