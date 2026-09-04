package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) Env {
	return func(k string) string { return m[k] }
}

func TestResolve_DefaultsOnAFreshMachine(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing config must not be an error: %v", err)
	}
	r, err := Resolve(f, envOf(nil), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if r.APIURL != DefaultAPIURL {
		t.Errorf("APIURL = %q, want production default %q", r.APIURL, DefaultAPIURL)
	}
	if r.Source["api_url"] != "default" {
		t.Errorf("source = %q, want default", r.Source["api_url"])
	}
	if r.Org != "" || r.Region != "" {
		t.Errorf("org/region must be empty with no config, got %q/%q", r.Org, r.Region)
	}
}

func TestResolve_Precedence_FlagBeatsEnvBeatsContext(t *testing.T) {
	f := &File{
		CurrentContext: "staging",
		Contexts: map[string]Context{
			"staging": {APIURL: "https://staging.cloud.co.zm/api/v1", Org: "ctx-org", Region: "ctx-region"},
		},
	}
	env := envOf(map[string]string{"CLOUD_API_URL": "https://env.example/api/v1", "CLOUD_ORG": "env-org"})

	// context only
	r, err := Resolve(f, envOf(nil), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if r.APIURL != "https://staging.cloud.co.zm/api/v1" || r.Source["api_url"] != "context" {
		t.Errorf("context should win with nothing else set: %q (%s)", r.APIURL, r.Source["api_url"])
	}
	// env beats context
	r, err = Resolve(f, env, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if r.APIURL != "https://env.example/api/v1" || r.Org != "env-org" {
		t.Errorf("env should beat context: %q / %q", r.APIURL, r.Org)
	}
	if r.Region != "ctx-region" {
		t.Errorf("region unset in env must fall through to context, got %q", r.Region)
	}
	// flag beats env
	r, err = Resolve(f, env, Overrides{APIURL: "https://flag.example/api/v1", Org: "flag-org"})
	if err != nil {
		t.Fatal(err)
	}
	if r.APIURL != "https://flag.example/api/v1" || r.Org != "flag-org" || r.Source["org"] != "flag" {
		t.Errorf("flag should beat env: %q / %q", r.APIURL, r.Org)
	}
}

func TestResolve_TrailingSlashIsNormalised(t *testing.T) {
	r, err := Resolve(&File{}, envOf(map[string]string{"CLOUD_API_URL": "https://cloud.co.zm/api/v1/"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(r.APIURL, "/") {
		t.Errorf("trailing slash must be stripped so paths join cleanly: %q", r.APIURL)
	}
}

func TestResolve_UnknownContextIsAnError(t *testing.T) {
	_, err := Resolve(&File{Contexts: map[string]Context{}}, envOf(nil), Overrides{Context: "nope"})
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("want an error naming the missing context, got %v", err)
	}
}

func TestResolve_ContextFromEnv(t *testing.T) {
	f := &File{Contexts: map[string]Context{"ci": {Org: "ci-org"}}}
	r, err := Resolve(f, envOf(map[string]string{"CLOUD_CONTEXT": "ci"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if r.ContextName != "ci" || r.Org != "ci-org" {
		t.Errorf("CLOUD_CONTEXT should select the context: %q / %q", r.ContextName, r.Org)
	}
}

// The platform 301s GET/HEAD from retired hosts, which would turn every POST
// into a GET. The CLI must refuse up front with a message naming the fix.
func TestValidateAPIURL_RefusesRetiredHosts(t *testing.T) {
	for _, u := range []string{
		"https://swiftcloud.co.zm/api/v1",
		"https://www.swiftcloud.co.zm/api/v1",
		"https://swiftcloud.africa/api/v1",
		"https://SwiftCloud.Africa/api/v1",
	} {
		err := ValidateAPIURL(u)
		if err == nil {
			t.Errorf("%s: want refusal, got nil", u)
			continue
		}
		if !strings.Contains(err.Error(), DefaultAPIURL) {
			t.Errorf("%s: error should name the replacement, got %v", u, err)
		}
	}
}

func TestValidateAPIURL_RequiresHTTPSExceptLoopback(t *testing.T) {
	if err := ValidateAPIURL("http://cloud.co.zm/api/v1"); err == nil {
		t.Error("plain http to a real host must be refused: it would send a bearer token in the clear")
	}
	for _, ok := range []string{
		"http://localhost:5173/api/v1",
		"http://127.0.0.1:5173/api/v1",
		"http://app.localhost/api/v1",
		"https://cloud.co.zm/api/v1",
		"https://staging.cloud.co.zm/api/v1",
	} {
		if err := ValidateAPIURL(ok); err != nil {
			t.Errorf("%s should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "not a url", "ftp://cloud.co.zm/api/v1", "cloud.co.zm/api/v1"} {
		if err := ValidateAPIURL(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestSaveLoad_RoundTripWithOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	in := &File{CurrentContext: "prod", Contexts: map[string]Context{"prod": {Org: "acme", Region: "zm-lusaka-central-1"}}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("config must be owner-only, got %o", perm)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.CurrentContext != "prod" || out.Contexts["prod"].Region != "zm-lusaka-central-1" {
		t.Errorf("round trip lost data: %+v", out)
	}
}

func TestLoad_MalformedFileIsAnErrorNamingThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("contexts: [not a map"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("want a parse error naming %s, got %v", path, err)
	}
}

func TestDir_HonoursExplicitOverrideAndXDG(t *testing.T) {
	d, err := Dir(envOf(map[string]string{"CLOUD_CONFIG_DIR": "/tmp/x"}))
	if err != nil || d != "/tmp/x" {
		t.Errorf("CLOUD_CONFIG_DIR should win: %q %v", d, err)
	}
	d, err = Dir(envOf(map[string]string{"XDG_CONFIG_HOME": "/xdg"}))
	if err != nil || d != filepath.Join("/xdg", "cloud") {
		t.Errorf("XDG_CONFIG_HOME should be used: %q %v", d, err)
	}
}
