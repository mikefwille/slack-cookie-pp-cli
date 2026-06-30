package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"slack-cookie-pp-cli/internal/config"
)

// authTestServer returns an httptest server whose /auth.test responds with the
// given body; every other path returns a bare 200 so the doctor reachability
// probe is satisfied.
func authTestServer(t *testing.T, authTestBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "auth.test") {
			_, _ = w.Write([]byte(authTestBody))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoctorAuthValidSession(t *testing.T) {
	isolateAuthHome(t)
	srv := authTestServer(t, `{"ok":true,"team":"example","user":"example-user"}`)
	if err := config.SaveAccount("work", config.Account{Workspace: srv.URL, Xoxc: "xoxc-x", DCookie: "d"}, true); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "doctor", "--account", "work", "--json")
	if err != nil {
		t.Fatalf("doctor should exit 0 for a valid session, got %v", err)
	}
	if !strings.Contains(out, "authenticated as example-user in example") {
		t.Errorf("expected identity verdict, got: %s", out)
	}
}

func TestDoctorAuthInvalidSessionExitsNonZero(t *testing.T) {
	isolateAuthHome(t)
	srv := authTestServer(t, `{"ok":false,"error":"invalid_auth"}`)
	if err := config.SaveAccount("work", config.Account{Workspace: srv.URL, Xoxc: "xoxc-stale", DCookie: "d"}, true); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "doctor", "--account", "work", "--json")
	if ExitCode(err) != 4 {
		t.Fatalf("invalid session should exit 4 (auth), got %d (err=%v)", ExitCode(err), err)
	}
	if !strings.Contains(out, "invalid or rotated") {
		t.Errorf("expected invalidation verdict, got: %s", out)
	}
}

func TestDoctorAllAccountsMixed(t *testing.T) {
	isolateAuthHome(t)
	okSrv := authTestServer(t, `{"ok":true,"team":"example","user":"example-user"}`)
	badSrv := authTestServer(t, `{"ok":false,"error":"invalid_auth"}`)
	_ = config.SaveAccount("work", config.Account{Workspace: okSrv.URL, Xoxc: "xoxc-ok", DCookie: "d"}, true)
	_ = config.SaveAccount("personal", config.Account{Workspace: badSrv.URL, Xoxc: "xoxc-bad", DCookie: "d"}, false)

	out, _, err := runCLI(t, "", "doctor", "--all-accounts", "--json")
	if ExitCode(err) != 4 {
		t.Fatalf("mixed accounts (one invalid) should exit 4, got %d", ExitCode(err))
	}
	if !strings.Contains(out, "example-user") {
		t.Errorf("expected the valid account's identity in the report, got: %s", out)
	}
}
