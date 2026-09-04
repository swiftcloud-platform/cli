package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func freshConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLOUD_CONFIG_DIR", dir)
	t.Setenv("CLOUD_API_URL", "")
	t.Setenv("CLOUD_ORG", "")
	t.Setenv("CLOUD_REGION", "")
	t.Setenv("CLOUD_CONTEXT", "")
	t.Setenv("CLOUD_TOKEN", "")
	return dir
}

func TestContext_SetUseListCurrent(t *testing.T) {
	dir := freshConfig(t)
	if _, err := run(t, "context", "set", "local", "--api-url", "http://localhost:5173/api/v1", "--org", "platform"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "context", "set", "prod", "--org", "acme", "--region", "zm-lusaka-central-1"); err != nil {
		t.Fatal(err)
	}
	// First context created becomes current automatically.
	out, _ := run(t, "context", "current")
	if strings.TrimSpace(out) != "local" {
		t.Errorf("first context should be current, got %q", out)
	}
	if _, err := run(t, "context", "use", "prod"); err != nil {
		t.Fatal(err)
	}
	out, _ = run(t, "context", "list")
	if !strings.Contains(out, "* ") || !strings.Contains(out, "prod") || !strings.Contains(out, "(default)") {
		t.Errorf("list must mark the current context and show the default API:\n%s", out)
	}
	out, _ = run(t, "context", "list", "-q")
	if out != "local\nprod\n" {
		t.Errorf("quiet list = %q", out)
	}
	// The file is owner-only and lives where config.Dir says.
	st, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("config.yaml perms: %v %v", st, err)
	}
}

func TestContext_ResolvesIntoCommands(t *testing.T) {
	freshConfig(t)
	srv := fakePlatform(t)
	if _, err := run(t, "context", "set", "local", "--api-url", srv.URL+"/api/v1", "--org", "acme", "--use"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUD_TOKEN", "owner-token")
	out, err := run(t, "app", "list", "-q")
	if err != nil || out != "web\n" {
		t.Errorf("the current context must supply API URL and org: %q %v", out, err)
	}
}

func TestContext_SetPartialUpdateKeepsOtherFields(t *testing.T) {
	freshConfig(t)
	_, _ = run(t, "context", "set", "prod", "--org", "acme", "--region", "r1")
	if _, err := run(t, "context", "set", "prod", "--region", "r2"); err != nil {
		t.Fatal(err)
	}
	out, _ := run(t, "context", "list", "-o", "json")
	if !strings.Contains(out, `"org": "acme"`) || !strings.Contains(out, `"region": "r2"`) {
		t.Errorf("partial set must keep org and change region:\n%s", out)
	}
	// Clear a field explicitly.
	_, _ = run(t, "context", "set", "prod", "--region", "")
	out, _ = run(t, "context", "list", "-o", "json")
	if !strings.Contains(out, `"region": ""`) {
		t.Errorf("empty value must clear the field:\n%s", out)
	}
}

func TestContext_SetRefusesRetiredOrInsecureAPI(t *testing.T) {
	freshConfig(t)
	for _, bad := range []string{"https://swiftcloud.co.zm/api/v1", "http://cloud.co.zm/api/v1", "not a url"} {
		_, err := run(t, "context", "set", "x", "--api-url", bad)
		if err == nil || ExitCode(err) != ExitUsage {
			t.Errorf("%q must be refused as usage, got %v", bad, err)
		}
	}
	out, _ := run(t, "context", "list", "-q")
	if strings.Contains(out, "x") {
		t.Error("a refused context must not be written")
	}
}

func TestContext_UseUnknownAndDelete(t *testing.T) {
	freshConfig(t)
	if _, err := run(t, "context", "use", "nope"); err == nil || !strings.Contains(err.Error(), "cloud context list") {
		t.Errorf("unknown context must point at list, got %v", err)
	}
	_, _ = run(t, "context", "set", "a", "--org", "acme", "--use")
	if _, err := run(t, "context", "delete", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "context", "current"); err == nil {
		t.Error("deleting the current context must leave none current")
	}
}

// Context commands must work even when the current context is broken — that is
// usually why someone is running them.
func TestContext_WorksWithBrokenCurrentContext(t *testing.T) {
	dir := freshConfig(t)
	_ = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("current_context: gone\ncontexts:\n  ok:\n    org: acme\n"), 0o600)
	if _, err := run(t, "context", "list"); err != nil {
		t.Errorf("list must not need the current context to resolve: %v", err)
	}
	if _, err := run(t, "context", "use", "ok"); err != nil {
		t.Errorf("use must repair a broken current context: %v", err)
	}
	// And a normal command now resolves again.
	if _, err := run(t, "completion", "bash"); err != nil {
		t.Errorf("after repair, resolution must succeed: %v", err)
	}
}

func TestContext_SetWithNothingToChangeOnExisting(t *testing.T) {
	freshConfig(t)
	_, _ = run(t, "context", "set", "a", "--org", "acme")
	if _, err := run(t, "context", "set", "a"); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("set on an existing context with no flags must say so, got %v", err)
	}
	if _, err := run(t, "context", "set", "b"); err != nil {
		t.Errorf("creating an empty new context is fine: %v", err)
	}
}
