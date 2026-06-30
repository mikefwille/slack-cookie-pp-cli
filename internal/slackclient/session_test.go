package slackclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const bootFixture = `<!DOCTYPE html><html><head><script>
var boot_data = {"team_id":"T123","api_token":"xoxc-123-456-abcDEF","no_login":false};
</script></head><body></body></html>`

func TestExtractXoxcToken(t *testing.T) {
	got, err := ExtractXoxcToken(bootFixture)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "xoxc-123-456-abcDEF" {
		t.Errorf("token = %q", got)
	}

	if _, err := ExtractXoxcToken(`<html>no token here</html>`); err == nil {
		t.Error("expected error when no token present")
	}
}

func TestMintXoxcFromCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "d=xoxd-abc") {
			t.Errorf("d cookie not forwarded: %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(bootFixture))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := MintXoxcFromCookie(ctx, srv.Client(), srv.URL, "xoxd-abc", "xoxd-s")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if got != "xoxc-123-456-abcDEF" {
		t.Errorf("minted token = %q", got)
	}
}

func TestMintXoxcFromCookieNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>logged out</html>`))
	}))
	defer srv.Close()
	if _, err := MintXoxcFromCookie(context.Background(), srv.Client(), srv.URL, "xoxd-x", ""); err == nil {
		t.Error("expected error when payload has no token")
	}
}

func TestMintXoxcFromCookieHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := MintXoxcFromCookie(context.Background(), srv.Client(), srv.URL, "xoxd-x", ""); err == nil {
		t.Error("expected error on HTTP 401")
	}
}
