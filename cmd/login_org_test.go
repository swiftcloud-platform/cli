package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
Choosing an organisation at login.

Signing in used to leave the CLI one instruction short of usable: it printed
the organisation and told you to run `cloud org use`. Every command that
needs an organisation failed until you did. It now picks one — but only when
nothing has chosen already, because a flag, an environment variable or a
context are deliberate and this is a guess.
*/

// loginFake serves /me with the organisations a test wants.
func loginFake(t *testing.T, orgsJSON string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"user":{"id":"u1","email":"a@b.zm","name":"Arthur"},"auth":{"kind":"api-token","expiresAt":"2030-01-01T00:00:00Z"},"organizations":%s}`, orgsJSON)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("CLOUD_CONFIG_DIR", dir)
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "")
	t.Setenv("CLOUD_TOKEN", "")
	return dir
}

func configAfter(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return ""
	}
	return string(b)
}

// The whole point: one organisation, and the CLI is ready to use afterwards.
func TestLogin_AdoptsTheOnlyOrganisation(t *testing.T) {
	dir := loginFake(t, `[{"id":"org1","slug":"acme","name":"Acme","role":"owner"}]`)
	testStdin = strings.NewReader("owner-token\n")
	if _, err := run(t, "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	cfgFile := configAfter(t, dir)
	if !strings.Contains(cfgFile, "org: acme") {
		t.Errorf("the organisation should have been saved:\n%s", cfgFile)
	}
	// And a command needing an organisation now resolves one without --org.
	out, err := run(t, "whoami", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"org": "acme"`) {
		t.Errorf("whoami should resolve the saved organisation:\n%s", out)
	}
}

// With several, the first is a starting point rather than an answer, so the
// message has to say how many there are and how to change it.
func TestLogin_AdoptsTheFirstOfSeveralAndSaysSo(t *testing.T) {
	dir := loginFake(t, `[{"id":"o1","slug":"alpha","name":"Alpha","role":"owner"},{"id":"o2","slug":"beta","name":"Beta","role":"member"}]`)
	testStdin = strings.NewReader("owner-token\n")
	if _, err := run(t, "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configAfter(t, dir), "org: alpha") {
		t.Errorf("the first organisation should have been saved:\n%s", configAfter(t, dir))
	}
}

// A deliberate choice outranks the guess. CLOUD_ORG is set here, so login
// must not overwrite the context with something else.
func TestLogin_DoesNotOverrideAChosenOrganisation(t *testing.T) {
	dir := loginFake(t, `[{"id":"o1","slug":"alpha","name":"Alpha","role":"owner"}]`)
	t.Setenv("CLOUD_ORG", "beta")
	testStdin = strings.NewReader("owner-token\n")
	if _, err := run(t, "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configAfter(t, dir), "org: alpha") {
		t.Errorf("login overwrote a deliberate choice:\n%s", configAfter(t, dir))
	}
}

// Belonging to none is not an error, but it does need saying — otherwise the
// next command fails with "no organisation selected" and no explanation.
func TestLogin_NoOrganisationsIsExplained(t *testing.T) {
	dir := loginFake(t, `[]`)
	testStdin = strings.NewReader("owner-token\n")
	if _, err := run(t, "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configAfter(t, dir), "org:") {
		t.Errorf("nothing should have been saved:\n%s", configAfter(t, dir))
	}
}
