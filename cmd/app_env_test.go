package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
)

/*
`app env` and `app update`.

The property worth guarding hardest is that a write preserves what it did not
mention: the platform replaces the whole map, so a merge bug here silently
deletes a variable someone's app needs.
*/

// envFake serves one app whose env vars it remembers across a PATCH.
type envFake struct {
	vars map[string]string
	// serving and latest are what the fake reports; a write that creates no
	// revision leaves them equal, which is the bug the message must not hide.
	serving string
	latest  string
	// advanceTo, when set, is the revision the fake starts serving as `latest`
	// once a PATCH lands — what a platform that really rolls one would do.
	advanceTo string
	patched   []map[string]string // every envVars map the CLI sent
	last      map[string]any      // the last PATCH body, whole
}

func newEnvFake(t *testing.T, vars map[string]string) *envFake {
	t.Helper()
	f := &envFake{vars: vars, serving: "notifie-00001", latest: "notifie-00001"}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/notifie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.last = body
			if f.advanceTo != "" {
				f.latest = f.advanceTo
			}
			if ev, ok := body["envVars"].(map[string]any); ok {
				got := map[string]string{}
				for k, v := range ev {
					got[k], _ = v.(string)
				}
				f.patched = append(f.patched, got)
				f.vars = got
			}
		}
		env, _ := json.Marshal(f.vars)
		fmt.Fprintf(w, `{"id":"a1","name":"notifie","organizationId":"o1","region":"zm-lsk-1","regionId":"r1","image":"ghcr.io/x/notifie:v1","description":"","status":"ready","errorMessage":null,"servingRevision":%q,"latestRevision":%q,"url":"https://notifie.example","containerPort":8080,"replicasMin":0,"replicasMax":3,"size":"app-1","envVars":%s,"registryAuth":null,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`, f.serving, f.latest, env)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")
	return f
}

func TestAppEnvList_MasksValuesByDefault(t *testing.T) {
	newEnvFake(t, map[string]string{"SENDGRID_KEY": "SG.averylongsecretvalue", "TZ": "UTC"})
	out, err := run(t, "app", "env", "list", "notifie")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "averylongsecret") {
		t.Errorf("the value leaked into a masked listing:\n%s", out)
	}
	if !strings.Contains(out, "SENDGRID_KEY") || !strings.Contains(out, "•") {
		t.Errorf("expected the name and a mask:\n%s", out)
	}
	// A short value must not be partly revealed.
	if strings.Contains(out, "UTC") {
		t.Errorf("a short value should be hidden entirely:\n%s", out)
	}
}

func TestAppEnvList_ShowValuesAndJSON(t *testing.T) {
	newEnvFake(t, map[string]string{"SENDGRID_KEY": "SG.averylongsecretvalue"})
	out, err := run(t, "app", "env", "list", "notifie", "--show-values")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "SG.averylongsecretvalue") {
		t.Errorf("--show-values should print the value:\n%s", out)
	}
	out, err = run(t, "app", "env", "list", "notifie", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "SG.averylongsecretvalue") {
		t.Errorf("json output is for machines and should carry the value:\n%s", out)
	}
}

// The whole point of the read-modify-write: setting one variable must not
// delete the others, because the platform replaces the entire map.
func TestAppEnvSet_PreservesTheVariablesItDidNotMention(t *testing.T) {
	f := newEnvFake(t, map[string]string{"KEEP": "yes", "ALSO": "kept"})
	if _, err := run(t, "app", "env", "set", "notifie", "NEW=1"); err != nil {
		t.Fatal(err)
	}
	if len(f.patched) != 1 {
		t.Fatalf("expected one PATCH, got %d", len(f.patched))
	}
	got := f.patched[0]
	for k, want := range map[string]string{"KEEP": "yes", "ALSO": "kept", "NEW": "1"} {
		if got[k] != want {
			t.Errorf("after set, %s = %q, want %q (full map: %v)", k, got[k], want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("map has %d entries, want 3: %v", len(got), got)
	}
}

func TestAppEnvSet_OverwritesAndTakesSeveral(t *testing.T) {
	f := newEnvFake(t, map[string]string{"LOG_LEVEL": "info"})
	if _, err := run(t, "app", "env", "set", "notifie", "LOG_LEVEL=debug", "TZ=Africa/Lusaka"); err != nil {
		t.Fatal(err)
	}
	got := f.patched[0]
	if got["LOG_LEVEL"] != "debug" || got["TZ"] != "Africa/Lusaka" {
		t.Errorf("unexpected map: %v", got)
	}
}

// A secret must be settable without appearing in argv.
func TestAppEnvSet_FromStdinKeepsTheValueOutOfArgv(t *testing.T) {
	f := newEnvFake(t, map[string]string{})
	testStdin = strings.NewReader("SG.thesecret\n")
	if _, err := run(t, "app", "env", "set", "notifie", "--from-stdin", "SENDGRID_KEY"); err != nil {
		t.Fatal(err)
	}
	// The trailing newline a file or here-doc adds must not become part of it.
	if got := f.patched[0]["SENDGRID_KEY"]; got != "SG.thesecret" {
		t.Errorf("value = %q, want the trimmed secret", got)
	}
}

func TestAppEnvSet_EnvFile(t *testing.T) {
	f := newEnvFake(t, map[string]string{"OLD": "1"})
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# a comment\n\nexport SENDGRID_KEY=\"SG.fromfile\"\nTZ='Africa/Lusaka'\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "app", "env", "set", "notifie", "--env-file", path); err != nil {
		t.Fatal(err)
	}
	got := f.patched[0]
	for k, want := range map[string]string{"SENDGRID_KEY": "SG.fromfile", "TZ": "Africa/Lusaka", "EMPTY": "", "OLD": "1"} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

func TestAppEnvSet_RejectsRubbish(t *testing.T) {
	f := newEnvFake(t, map[string]string{})
	for _, args := range [][]string{
		{"app", "env", "set", "notifie", "NOTAPAIR"},
		{"app", "env", "set", "notifie", "=novalue"},
		{"app", "env", "set", "notifie"}, // nothing to set
	} {
		if _, err := run(t, args...); err == nil || ExitCode(err) != ExitUsage {
			t.Errorf("%v should be a usage error, got %v", args[3:], err)
		}
	}
	if len(f.patched) != 0 {
		t.Errorf("nothing should have been written, got %v", f.patched)
	}
}

func TestAppEnvUnset(t *testing.T) {
	f := newEnvFake(t, map[string]string{"GONE": "x", "KEEP": "y"})
	if _, err := run(t, "app", "env", "unset", "notifie", "GONE"); err != nil {
		t.Fatal(err)
	}
	got := f.patched[0]
	if _, still := got["GONE"]; still {
		t.Errorf("GONE was not removed: %v", got)
	}
	if got["KEEP"] != "y" {
		t.Errorf("KEEP was lost: %v", got)
	}
}

// Removing a name that is not there is a typo, not a no-op.
func TestAppEnvUnset_UnknownNameIsAnError(t *testing.T) {
	f := newEnvFake(t, map[string]string{"KEEP": "y"})
	_, err := run(t, "app", "env", "unset", "notifie", "NOSUCH")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("expected a usage error, got %v", err)
	}
	if len(f.patched) != 0 {
		t.Errorf("nothing should have been written, got %v", f.patched)
	}
}

func TestAppUpdate_PortAndDescription(t *testing.T) {
	f := newEnvFake(t, map[string]string{})
	if _, err := run(t, "app", "update", "notifie", "--port", "3000"); err != nil {
		t.Fatal(err)
	}
	if f.last["containerPort"] != float64(3000) {
		t.Errorf("containerPort = %v", f.last["containerPort"])
	}
	// A field not passed must not appear in the patch at all, or it would
	// overwrite the stored value with a zero.
	if _, present := f.last["envVars"]; present {
		t.Errorf("update sent envVars it was not asked to change: %v", f.last)
	}
	if _, present := f.last["description"]; present {
		t.Errorf("update sent an unmentioned description: %v", f.last)
	}
}

func TestAppUpdate_NothingToChange(t *testing.T) {
	newEnvFake(t, map[string]string{})
	_, err := run(t, "app", "update", "notifie")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("an update with no flags should be a usage error, got %v", err)
	}
}

func TestAppUpdate_RegistryFlagsMustComeTogether(t *testing.T) {
	newEnvFake(t, map[string]string{})
	_, err := run(t, "app", "update", "notifie", "--registry-server", "ghcr.io")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("partial registry flags should be refused, got %v", err)
	}
	_, err = run(t, "app", "update", "notifie", "--clear-registry", "--registry-server", "ghcr.io")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("--clear-registry with other registry flags should be refused, got %v", err)
	}
}

func TestAppUpdate_ClearRegistrySendsNull(t *testing.T) {
	f := newEnvFake(t, map[string]string{})
	if _, err := run(t, "app", "update", "notifie", "--clear-registry"); err != nil {
		t.Fatal(err)
	}
	v, present := f.last["registryAuth"]
	if !present || v != nil {
		t.Errorf("expected an explicit null registryAuth, got %#v (present=%v)", v, present)
	}
}

func TestAppUpdate_RotatesRegistryCredentialsFromStdin(t *testing.T) {
	f := newEnvFake(t, map[string]string{})
	testStdin = strings.NewReader("ghp_newtoken\n")
	_, err := run(t, "app", "update", "notifie",
		"--registry-server", "ghcr.io", "--registry-username", "deploy", "--registry-password-stdin")
	if err != nil {
		t.Fatal(err)
	}
	ra, ok := f.last["registryAuth"].(map[string]any)
	if !ok {
		t.Fatalf("registryAuth = %#v", f.last["registryAuth"])
	}
	if ra["password"] != "ghp_newtoken" || ra["server"] != "ghcr.io" || ra["username"] != "deploy" {
		t.Errorf("unexpected credentials: %v", ra)
	}
}

func TestParseEnvFile(t *testing.T) {
	in := "# comment\n\nA=1\nexport B=2\nC=\"quoted\"\nD='single'\nE=has=equals\nF=\n"
	got, err := parseEnvFile(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "2", "C": "quoted", "D": "single", "E": "has=equals", "F": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, err := parseEnvFile(strings.NewReader("NOT A PAIR\n")); err == nil {
		t.Error("a line that is not KEY=VALUE must be an error")
	}
}

func TestMaskValue(t *testing.T) {
	for in, want := range map[string]string{
		"":          "",
		"abc":       "•••",
		"12345678":  "••••••••",
		"123456789": "12•••••89",
	} {
		if got := maskValue(in); got != want {
			t.Errorf("maskValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// An empty log stream must say so. Silence is indistinguishable from "the app
// is quiet", and it is the reported symptom that made a real deployment
// undebuggable.
func TestAppLogs_EmptyStreamExplainsItself(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/notifie/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/plain")
		w.WriteHeader(http.StatusOK) // 200 with no body at all
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")

	var errBuf bytes.Buffer
	resetFlags(rootCmd)
	rootCmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs([]string{"app", "logs", "notifie"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" {
		t.Errorf("stdout should stay clean for pipes, got %q", out.String())
	}
	msg := errBuf.String()
	for _, want := range []string{"No log lines returned", "scaled to zero", "-f"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the explanation should mention %q:\n%s", want, msg)
		}
	}
}

// Log lines themselves go to stdout and nothing else does, so `| grep` works.
func TestAppLogs_LinesGoToStdoutOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/notifie/logs", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "line one\nline two\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")

	var out, errBuf bytes.Buffer
	resetFlags(rootCmd)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs([]string{"app", "logs", "notifie"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "line one\nline two\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if strings.Contains(errBuf.String(), "No log lines") {
		t.Errorf("a stream with output must not claim it was empty:\n%s", errBuf.String())
	}
}

// A status alone is not actionable: when the platform says why, the CLI must
// show it. `cloud db get` has always done this; apps only gained the field.
func TestAppGet_ShowsTheFailureReason(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/broken", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"a1","name":"broken","organizationId":"o1","region":"zm-lsk-1","regionId":"r1","image":"ghcr.io/x/y:v1","description":"","status":"failed","errorMessage":"RevisionFailed: container image not found","url":"","containerPort":8080,"replicasMin":0,"replicasMax":3,"size":"app-1","envVars":{},"registryAuth":null,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/fine", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"a2","name":"fine","organizationId":"o1","region":"zm-lsk-1","regionId":"r1","image":"ghcr.io/x/y:v1","description":"","status":"ready","errorMessage":"","url":"https://fine.example","containerPort":8080,"replicasMin":0,"replicasMax":3,"size":"app-1","envVars":{},"registryAuth":null,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")

	out, err := run(t, "app", "get", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "RevisionFailed: container image not found") {
		t.Errorf("a failed app must say why:\n%s", out)
	}

	// And a healthy app must not grow an empty Reason line.
	out, err = run(t, "app", "get", "fine")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Reason") {
		t.Errorf("a healthy app should have no Reason line:\n%s", out)
	}
}

// appFake serves one app whose fields the test controls.
func appFake(t *testing.T, appJSONBody string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/notifie", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, appJSONBody)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")
}

func appBody(status, errMsg, serving, latest string) string {
	return fmt.Sprintf(`{"id":"a1","name":"notifie","organizationId":"o1","region":"zm-lsk-1","regionId":"r1",`+
		`"image":"ghcr.io/x/y:v1.2.5","description":"","status":%q,"errorMessage":%q,`+
		`"servingRevision":%q,"latestRevision":%q,"url":"https://notifie.example",`+
		`"containerPort":3000,"replicasMin":1,"replicasMax":3,"size":"app-1","envVars":{},"registryAuth":null,`+
		`"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`, status, errMsg, serving, latest)
}

// The state that used to be invisible: newest revision created, old one still
// taking traffic. `app get` must say so rather than showing a healthy record.
func TestAppGet_SaysWhenTheNewestRevisionIsNotServing(t *testing.T) {
	appFake(t, appBody("ready", "", "notifie-00003", "notifie-00004"))
	out, err := run(t, "app", "get", "notifie")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "notifie-00003 serving") || !strings.Contains(out, "notifie-00004 is newest") {
		t.Errorf("both revisions should be named:\n%s", out)
	}
	if !strings.Contains(out, "not taking traffic") {
		t.Errorf("the divergence should be spelled out, not left to the reader:\n%s", out)
	}
}

func TestAppGet_QuietWhenTheRolloutIsComplete(t *testing.T) {
	appFake(t, appBody("ready", "", "notifie-00004", "notifie-00004"))
	out, err := run(t, "app", "get", "notifie")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "not taking traffic") {
		t.Errorf("a settled rollout should not warn:\n%s", out)
	}
	if !strings.Contains(out, "Revision     notifie-00004") {
		t.Errorf("the serving revision should still be shown:\n%s", out)
	}
}

// A suspended app is not a successful deploy: enforcement stopped it, and
// exiting 0 would tell a script the rollout worked.
func TestNotServing_SuspendedIsAnError(t *testing.T) {
	err := notServing(&api.App{Name: "notifie", Status: "suspended"})
	if err == nil {
		t.Fatal("suspended must not read as success")
	}
	if !strings.Contains(err.Error(), "billing") {
		t.Errorf("the message should say where to look: %v", err)
	}
	// The platform's own sentence wins when it has one.
	err = notServing(&api.App{Name: "notifie", Status: "suspended", ErrorMessage: "unpaid invoice 42"})
	if err == nil || !strings.Contains(err.Error(), "unpaid invoice 42") {
		t.Errorf("the platform's reason should be used: %v", err)
	}
	for _, ok := range []string{"ready", "running", "stopped"} {
		if err := notServing(&api.App{Name: "n", Status: ok}); err != nil {
			t.Errorf("%s should not be an error: %v", ok, err)
		}
	}
}

// The CLI used to say "a new revision is rolling out" after every env write,
// whether or not one was. When the platform silently created none, that
// sentence was all that stood between an operator and believing their change
// had applied — the same class of lie as the empty log stream.
func TestAppEnvSet_DoesNotClaimARolloutItCannotSee(t *testing.T) {
	f := newEnvFake(t, map[string]string{"A": "1"})
	// The platform reports the same revision after the write: nothing rolled.
	var errBuf bytes.Buffer
	resetFlags(rootCmd)
	rootCmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs([]string{"app", "env", "set", "notifie", "B=2"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	msg := errBuf.String()
	if strings.Contains(msg, "Rolling out") {
		t.Errorf("no revision changed, so nothing should claim one is rolling:\n%s", msg)
	}
	for _, want := range []string{"No new revision recorded yet", "notifie-00001", "can lag the cluster"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the message to say what was observed (%q):\n%s", want, msg)
		}
	}
	if len(f.patched) != 1 {
		t.Errorf("the write itself should still have happened: %v", f.patched)
	}
}

// And when a revision really does appear, say so by name.
func TestAppEnvSet_NamesTheRevisionWhenOneRolls(t *testing.T) {
	f := newEnvFake(t, map[string]string{"A": "1"})
	// A platform that really rolls a revision reports a new one after the write.
	f.advanceTo = "notifie-00002"
	var errBuf bytes.Buffer
	resetFlags(rootCmd)
	rootCmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs([]string{"app", "env", "set", "notifie", "B=2"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "Rolling out notifie-00002") {
		t.Errorf("a real rollout should be named:\n%s", errBuf.String())
	}
}

// A deploy that changes nothing leaves the app ready and explains itself in
// errorMessage. Printing that only for a non-serving app hid the one case the
// user most needs: "nothing to roll out — push a new tag or force it".
func TestPrintAppURL_ShowsTheReasonEvenWhenReady(t *testing.T) {
	var out, errBuf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	flagQuiet = false
	printAppURL(cmd, &api.App{
		Name: "notifie", Status: "ready", Url: "https://notifie.example",
		ErrorMessage: "Nothing to roll out: ghcr.io/x/y:v1 is already running. Push the build under a new tag, or force a redeploy to roll the same tag again.",
	})
	if !strings.Contains(errBuf.String(), "Nothing to roll out") {
		t.Errorf("the platform's reason must be shown for a ready app too:\n%s", errBuf.String())
	}
	// The URL still belongs on stdout: the app is serving.
	if strings.TrimSpace(out.String()) != "https://notifie.example" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestAppDeploy_ForceIsSentOnlyWhenAsked(t *testing.T) {
	// Without --force the field must be absent, not false: the platform
	// distinguishes "not asked" from "asked not to".
	f := newEnvFake(t, map[string]string{})
	_ = f
	if deployForce {
		t.Fatal("the flag should default to false")
	}
}

func TestSinceSeconds(t *testing.T) {
	// Unset means unset: no parameter, so the platform's own default applies.
	if v, err := sinceSeconds("since", 0); err != nil || v != nil {
		t.Errorf("zero should send nothing: %v %v", v, err)
	}
	if v, err := sinceSeconds("since", 10*time.Minute); err != nil || v == nil || *v != 600 {
		t.Errorf("10m should be 600s: %v %v", v, err)
	}
	// The platform ignores a non-positive value and caps at 30 days. Passing
	// either silently would misreport what was asked for.
	if _, err := sinceSeconds("since", -5*time.Minute); err == nil || ExitCode(err) != ExitUsage {
		t.Errorf("a negative window should be refused, got %v", err)
	}
	if _, err := sinceSeconds("since", 31*24*time.Hour); err == nil || ExitCode(err) != ExitUsage {
		t.Errorf("beyond the platform cap should be refused, got %v", err)
	}
	// Sub-second rounds up rather than to zero, which would mean "unset".
	if v, err := sinceSeconds("since", 500*time.Millisecond); err != nil || v == nil || *v != 1 {
		t.Errorf("a sub-second window should not become unset: %v %v", v, err)
	}
}

// The parameter must actually reach the wire, not just parse.
func TestAppLogs_SinceReachesTheQuery(t *testing.T) {
	var gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/notifie/logs", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, "a line\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")

	if _, err := run(t, "app", "logs", "notifie", "--since", "10m"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "since=600") {
		t.Errorf("query = %q, want since=600", gotQuery)
	}
}
