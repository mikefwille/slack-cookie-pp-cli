// Hand-authored extension (survives regen): the single Slack session-cookie
// header builder, shared by the composed-auth config layer and the d→xoxc
// bootstrap exchange so the "d=…; d-s=…" format has one source of truth.

package cliutil

import "strings"

// CookieHeader builds the Slack session Cookie header value from the d (and
// optional d-s) cookie, tolerating values that already carry the "d="/"d-s="
// prefix. Returns "" when both are empty.
func CookieHeader(d, ds string) string {
	var parts []string
	if d = strings.TrimSpace(d); d != "" {
		parts = append(parts, "d="+strings.TrimPrefix(d, "d="))
	}
	if ds = strings.TrimSpace(ds); ds != "" {
		parts = append(parts, "d-s="+strings.TrimPrefix(ds, "d-s="))
	}
	return strings.Join(parts, "; ")
}
