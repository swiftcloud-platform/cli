package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cloud/internal/api"
	"cloud/internal/auth"
	"cloud/internal/config"
)

/*
Tests for the security fixes in the command layer: arguments that would
retarget a request, exit codes, waits that end suspended, escape sequences
from the platform, logout, the organisation default, login's approval URL,
and the console URL.
*/

// countingPlatform answers everything with 404 and counts what reached it.
func countingPlatform(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		problem(w, 404, "not-found", "nope")
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")
	return &hits
}

// `vm ssh-key remove web ..` deleted the VM; `app domain remove demo ..`
// deleted the app. Every such argument is refused before any request.
func TestResourceArgs_DotSegmentsNeverReachThePlatform(t *testing.T) {
	hits := countingPlatform(t)
	for _, args := range [][]string{
		{"vm", "ssh-key", "remove", "web", "..", "--yes"},
		{"vm", "ssh-key", "remove", "web", ".", "--yes"},
		{"vm", "ssh-key", "add", "..", "ssh-ed25519 AAAA"},
		{"app", "domain", "remove", "demo", "..", "--yes"},
		{"app", "get", "."},
		{"app", "delete", "..", "--yes"},
		{"app", "env", "unset", "..", "KEY"},
		{"app", "env", "set", "..", "A=1"},
		{"db", "backup", "list", ".."},
		{"db", "get", ""},
		{"dns", "records", "remove", "example.com", "..", "--yes"},
		{"vm", "ip", "get", ".."},
		{"vm", "console", ".."},
		{"storage", "bucket", "get", ".."},
		{"storage", "bucket", "domains", "remove", "pics", "..", "--yes"},
		{"storage", "ls", "s3://../x"},
		{"storage", "rm", "s3://./key"},
		{"org", "use", ".."},
	} {
		_, err := run(t, args...)
		if err == nil {
			t.Errorf("%v: want a refusal", args)
			continue
		}
		if got := ExitCode(err); got != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%v)", args, got, ExitUsage, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests reached the platform; none should have", n)
	}
}

// Local paths, KEY=VALUE pairs and key files are not resource names: "." is a
// perfectly good directory for `storage cp`.
func TestResourceArgs_NonNamesAreNotChecked(t *testing.T) {
	countingPlatform(t)
	for _, args := range [][]string{
		{"storage", "cp", ".", "s3://pics/"},
		{"storage", "sync", ".", "s3://pics/site"},
		{"app", "env", "set", "web", "DOTS=.."},
		{"vm", "ssh-key", "add", "web", "."},
	} {
		_, err := run(t, args...)
		if err != nil && strings.Contains(err.Error(), "is not a valid name") {
			t.Errorf("%v: refused as a resource name: %v", args, err)
		}
	}
}

// The exemption list names commands by path; a renamed command would make an
// entry silently stale.
func TestArgPolicyByPath_NamesRealCommands(t *testing.T) {
	for path := range argPolicyByPath {
		words := strings.Fields(path)[1:]
		c, rest, err := rootCmd.Find(words)
		if err != nil || len(rest) != 0 || c.CommandPath() != path {
			t.Errorf("%q does not name a command (found %v, rest %v, err %v)", path, c.CommandPath(), rest, err)
		}
	}
}

func TestOrgOverride_DotDotIsAUsageError(t *testing.T) {
	hits := countingPlatform(t)
	for _, args := range [][]string{{"--org", "..", "app", "list"}, {"--org", ".", "vm", "list"}} {
		_, err := run(t, args...)
		if ExitCode(err) != ExitUsage {
			t.Errorf("%v: exit %d (%v)", args, ExitCode(err), err)
		}
	}
	t.Setenv("CLOUD_ORG", "..")
	if _, err := run(t, "db", "list"); ExitCode(err) != ExitUsage {
		t.Errorf("CLOUD_ORG=..: exit %d (%v)", ExitCode(err), err)
	}
	if hits.Load() != 0 {
		t.Error("a request was sent")
	}
}

// The platform's detail is free text; it must not be prefix-matched into a
// different exit code than its type says.
func TestExitCode_PlatformTypeBeatsMessagePrefix(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&api.Error{Type: "forbidden", Detail: "invalid argument: your role"}, ExitDenied},
		{&api.Error{Type: "not-found", Detail: "accepts nothing"}, ExitMissing},
		{&api.Error{Type: "internal", Detail: "unknown flag in the worker"}, ExitError},
		{fmt.Errorf("could not reach x: %w", &api.PathError{Path: "/a/"}), ExitUsage},
		{errors.New("required flag(s) \"image\" not set"), ExitUsage},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// vm/db --wait must not exit 0 when the resource ends suspended.
func TestWait_SuspendedVMAndDatabaseExitNonZero(t *testing.T) {
	const suspendedVM = `{"id":"vm1","name":"web","organizationId":"org1","region":"zm-lsk-1","regionId":"r1","image":"ubuntu-24-04","size":"vm-1","description":"","status":"suspended","publicIp":"","privateIp":"","errorMessage":"billing","createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","api":"v1","database":"ok","worker":{"status":"ok","ageSeconds":1},"time":"2026-09-04T00:00:00Z"}`))
	})
	mux.HandleFunc("/api/v1/orgs/acme/vms/web/start", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
		_, _ = w.Write([]byte(strings.Replace(suspendedVM, `"suspended"`, `"starting"`, 1)))
	})
	mux.HandleFunc("/api/v1/orgs/acme/vms/web", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(suspendedVM))
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
	_, err := run(t, "vm", "start", "web", "--wait")
	if err == nil || !strings.Contains(err.Error(), "suspended") || ExitCode(err) == ExitOK {
		t.Errorf("vm start --wait on a suspended VM: %v", err)
	}

	suspendedDB := strings.Replace(strings.Replace(dbJSON, `"status":"ready"`, `"status":"suspended"`, 1), `"errorMessage":""`, `"errorMessage":"billing"`, 1)
	dbSetup(t, map[string]http.HandlerFunc{
		"/api/v1/orgs/acme/databases/orders": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(suspendedDB)) },
		"/api/v1/orgs/acme/databases/orders/start": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(202)
			_, _ = w.Write([]byte(strings.Replace(dbJSON, `"status":"ready"`, `"status":"starting"`, 1)))
		},
		"/api/v1/health": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok","api":"v1","database":"ok","worker":{"status":"ok","ageSeconds":1},"time":"2026-09-04T00:00:00Z"}`))
		},
	})
	_, err = run(t, "db", "start", "orders", "--wait")
	if err == nil || !strings.Contains(err.Error(), "suspended — billing") {
		t.Errorf("db start --wait on a suspended database: %v", err)
	}
}

func TestSuspendedErr_StoppedIsNotAnError(t *testing.T) {
	if suspendedErr("web", "stopped", "") != nil || suspendedErr("web", "ready", "") != nil {
		t.Error("only suspended is an error")
	}
	if err := suspendedErr("web\x1b[2J", "suspended", "x\x1b]0;t\x07"); err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("want a sanitised error, got %q", err)
	}
}

// A VM's fields come from the platform and are printed to a terminal.
func TestVMGet_StripsEscapeSequences(t *testing.T) {
	evil := strings.Replace(strings.Replace(vmJSON, `"description":"test VM"`, `"description":"x\u001b[2J\u001b]0;pwned\u0007"`, 1),
		`"errorMessage":""`, `"errorMessage":"e\u001b[31m"`, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/orgs/acme/vms/web", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(evil)) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")
	out, errOut, err := runWithStderr(t, "vm", "get", "web")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out+errOut, "\x1b\x07") {
		t.Errorf("escape sequences reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "pwned") {
		t.Errorf("the text itself should still be shown:\n%s", out)
	}
}

func TestWarnEnvTokenHost_OncePerRunAndOnlyOffDefault(t *testing.T) {
	saved := cfg
	t.Cleanup(func() { cfg = saved; envTokenWarned = false })
	env := &auth.Credential{Token: "secret-value", Kind: auth.KindEnv}

	for _, c := range []struct {
		url  string
		warn bool
	}{
		{config.DefaultAPIURL, false},
		{"https://CLOUD.co.zm./api/v1", false},
		{"http://127.0.0.1:5173/api/v1", false},
		{"https://staging.example/api/v1", true},
	} {
		envTokenWarned = false
		cfg = &config.Resolved{APIURL: c.url, Source: map[string]string{"api_url": "env"}}
		var b bytes.Buffer
		warnEnvTokenHost(&b, env)
		warnEnvTokenHost(&b, env)
		if got := strings.Count(b.String(), "warning:"); (got == 1) != c.warn || got > 1 {
			t.Errorf("%s: %d warnings: %q", c.url, got, b.String())
		}
		if strings.Contains(b.String(), "secret-value") {
			t.Fatal("the warning printed the token")
		}
	}
	// A stored credential is not CLOUD_TOKEN.
	envTokenWarned = false
	var b bytes.Buffer
	warnEnvTokenHost(&b, &auth.Credential{Token: "x", Kind: auth.KindSession})
	if b.Len() != 0 {
		t.Error("warned for a stored credential")
	}
}

// ── logout ──────────────────────────────────────────────────────────────────

func logoutFake(t *testing.T) (dir string, signOuts *atomic.Int32, apiURL string) {
	t.Helper()
	signOuts = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/sign-out" && r.Header.Get("Authorization") == "Bearer sess-token" {
			signOuts.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		w.WriteHeader(401)
	}))
	t.Cleanup(srv.Close)
	dir = t.TempDir()
	t.Setenv("CLOUD_CONFIG_DIR", dir)
	t.Setenv("CLOUD_TOKEN", "")
	t.Setenv("CLOUD_ORG", "")
	apiURL = srv.URL + "/api/v1"
	t.Setenv("CLOUD_API_URL", apiURL)
	return dir, signOuts, apiURL
}

func TestLogout_EndsTheSessionOnTheServerThenForgetsIt(t *testing.T) {
	dir, signOuts, apiURL := logoutFake(t)
	s := &auth.Store{Dir: dir}
	if err := s.Save(apiURL, &auth.Credential{Token: "sess-token", Kind: auth.KindSession}); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runWithStderr(t, "logout")
	if err != nil {
		t.Fatal(err)
	}
	if signOuts.Load() != 1 {
		t.Errorf("sign-out calls = %d, want 1\n%s", signOuts.Load(), errOut)
	}
	if len(s.Hosts()) != 0 {
		t.Error("credential still stored")
	}
	if strings.Contains(errOut, "sess-token") {
		t.Fatal("logout printed the token")
	}
}

func TestLogout_ServerFailureStillForgetsLocally(t *testing.T) {
	dir, _, apiURL := logoutFake(t)
	s := &auth.Store{Dir: dir}
	_ = s.Save(apiURL, &auth.Credential{Token: "other-session", Kind: auth.KindSession})
	_, errOut, err := runWithStderr(t, "logout")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "could not end the session") || len(s.Hosts()) != 0 {
		t.Errorf("want a warning and a deleted credential:\n%s", errOut)
	}
}

func TestLogout_WarnsAboutCLOUD_TOKEN(t *testing.T) {
	logoutFake(t)
	t.Setenv("CLOUD_TOKEN", "sc_ci")
	_, errOut, err := runWithStderr(t, "logout")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "CLOUD_TOKEN is set") {
		t.Errorf("no CLOUD_TOKEN warning:\n%s", errOut)
	}
	if strings.Contains(errOut, "sc_ci") {
		t.Fatal("logout printed the token")
	}
}

func TestLogout_AllRemovesEveryHost(t *testing.T) {
	dir, _, apiURL := logoutFake(t)
	s := &auth.Store{Dir: dir}
	_ = s.Save(apiURL, &auth.Credential{Token: "sess-token", Kind: auth.KindSession})
	_ = s.Save("https://staging.cloud.co.zm/api/v1", &auth.Credential{Token: "b", Kind: auth.KindSession})
	if _, err := run(t, "logout", "--all"); err != nil {
		t.Fatal(err)
	}
	if len(s.Hosts()) != 0 {
		t.Errorf("left behind: %v", s.Hosts())
	}
}

// A retired host is refused for everything else, but logout only needs the
// host key, and must send nothing there.
func TestLogout_WorksForARetiredHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUD_CONFIG_DIR", dir)
	t.Setenv("CLOUD_TOKEN", "")
	t.Setenv("CLOUD_ORG", "")
	retired := "https://swiftcloud.co.zm/api/v1"
	t.Setenv("CLOUD_API_URL", retired)
	s := &auth.Store{Dir: dir}
	_ = s.Save(retired, &auth.Credential{Token: "old", Kind: auth.KindSession})
	if _, err := run(t, "logout"); err != nil {
		t.Fatalf("logout must work for a retired host: %v", err)
	}
	if len(s.Hosts()) != 0 {
		t.Error("credential still stored")
	}
	if _, err := run(t, "whoami"); ExitCode(err) != ExitUsage {
		t.Errorf("other commands must still refuse the retired host: %v", err)
	}
}

// ── org default ─────────────────────────────────────────────────────────────

func TestOrgUse_WarnsWhenCLOUD_ORGOverridesTheDefault(t *testing.T) {
	setup(t, "owner-token")
	t.Setenv("CLOUD_ORG", "acme")
	_, errOut, err := runWithStderr(t, "org", "use", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "CLOUD_ORG is set to acme") {
		t.Errorf("want a warning naming the override:\n%s", errOut)
	}
}

// An API URL from the environment is an override for this run, not a choice
// to save: persisting it would send every later command to that host.
func TestSaveDefaultOrg_DoesNotPersistAnEnvAPIURL(t *testing.T) {
	setup(t, "owner-token") // CLOUD_API_URL is the fake
	if _, err := run(t, "org", "use", "beta"); err != nil {
		t.Fatal(err)
	}
	f, _ := config.Load(filepath.Join(os.Getenv("CLOUD_CONFIG_DIR"), "config.yaml"))
	if got := f.Contexts["default"]; got.Org != "beta" || got.APIURL != "" {
		t.Errorf("context = %+v; want org beta and no API URL", got)
	}
}

func TestSaveDefaultOrg_PersistsAFlagAPIURLOnlyForANewContextAndSaysSo(t *testing.T) {
	setup(t, "owner-token")
	apiURL := os.Getenv("CLOUD_API_URL")
	t.Setenv("CLOUD_API_URL", "")
	_, errOut, err := runWithStderr(t, "--api-url", apiURL, "org", "use", "beta")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("CLOUD_CONFIG_DIR"), "config.yaml")
	f, _ := config.Load(path)
	if f.Contexts["default"].APIURL != apiURL || !strings.Contains(errOut, "Saved API URL") {
		t.Errorf("context = %+v\n%s", f.Contexts["default"], errOut)
	}

	// An existing context's API URL is never rewritten by an override.
	f.Contexts["default"] = config.Context{Org: "acme"}
	_ = config.Save(path, f)
	if _, err := run(t, "--api-url", apiURL, "org", "use", "beta"); err != nil {
		t.Fatal(err)
	}
	f, _ = config.Load(path)
	if f.Contexts["default"].APIURL != "" {
		t.Errorf("an existing context gained an API URL: %+v", f.Contexts["default"])
	}
}

// ── login ───────────────────────────────────────────────────────────────────

func deviceFake(t *testing.T, verification string) (opened *[]string) {
	t.Helper()
	opened = &[]string{}
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		v := strings.ReplaceAll(verification, "BASE", base)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "d", "user_code": "ABCD\x1b[2J1234"[:8], "verification_uri": v,
			"verification_uri_complete": v + "?user_code=ABCD1234", "expires_in": 0, "interval": 1,
		})
	})
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"sess","token_type":"Bearer","expires_in":600}`))
	})
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"id":"u1","email":"a@b.zm\u001b[2J"},"auth":{"kind":"session"},"organizations":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_TOKEN", "")
	t.Setenv("CLOUD_ORG", "")
	saved := openBrowser
	openBrowser = func(u string) error { *opened = append(*opened, u); return nil }
	t.Cleanup(func() { openBrowser = saved })
	return opened
}

func TestLogin_RefusesAnApprovalURLOnAnotherSite(t *testing.T) {
	opened := deviceFake(t, "https://evil.example/device")
	_, errOut, err := runWithStderr(t, "login")
	if err == nil || !strings.Contains(err.Error(), "refusing approval URL") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if len(*opened) != 0 || strings.Contains(errOut, "evil.example/device?") {
		t.Errorf("the URL was shown or opened: %v\n%s", *opened, errOut)
	}
}

func TestLogin_SaysWhereAndWarnsOffDefault(t *testing.T) {
	opened := deviceFake(t, "BASE/device")
	_, errOut, err := runWithStderr(t, "login")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "Signing in to 127.0.0.1") || !strings.Contains(errOut, "not the default API") {
		t.Errorf("must name the host and warn:\n%s", errOut)
	}
	if strings.Index(errOut, "Signing in to") > strings.Index(errOut, "Your code is") {
		t.Error("the host must be named before the code")
	}
	if len(*opened) != 1 || !strings.HasSuffix((*opened)[0], "/device?user_code=ABCD1234") {
		t.Errorf("opened %v", *opened)
	}
	if strings.ContainsAny(errOut, "\x1b") {
		t.Errorf("escape sequences printed:\n%q", errOut)
	}
}

// ── vm console ──────────────────────────────────────────────────────────────

func TestConsoleURL_EscapesTheID(t *testing.T) {
	got := consoleURL("https://cloud.co.zm/api/v1", "a/b?c")
	if got != "https://cloud.co.zm/dash/vms/a%2Fb%3Fc/console" {
		t.Errorf("consoleURL = %q", got)
	}
}

func TestVMConsole_PrintsTheURLAndReportsABrowserFailure(t *testing.T) {
	vmSetup(t, "owner-token")
	saved := openBrowser
	t.Cleanup(func() { openBrowser = saved })
	openBrowser = func(string) error { return errors.New("no xdg-open") }
	out, err := run(t, "vm", "console", "web")
	if err == nil || ExitCode(err) == ExitOK || !strings.Contains(err.Error(), "no xdg-open") {
		t.Errorf("a browser failure must not exit 0: %v", err)
	}
	if !strings.Contains(out, "/dash/vms/vm1/console") {
		t.Errorf("the URL must always be printed: %q", out)
	}
}
