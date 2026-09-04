package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// run executes the root command with a clean flag state. Cobra flags are
// package-level variables, so without the reset a value set by one test
// would leak into the next.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), err
}

func TestVersion_WorksWithoutAnyConfig(t *testing.T) {
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	out, err := run(t, "version")
	if err != nil {
		t.Fatalf("version must never need config or network: %v", err)
	}
	if !strings.HasPrefix(out, "cloud ") {
		t.Errorf("unexpected output %q", out)
	}
}

func TestBadOutputFormat_IsAUsageError(t *testing.T) {
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	// `completion` is a built-in command that goes through the persistent pre-run.
	_, err := run(t, "--output", "xml", "completion", "bash")
	if err == nil {
		t.Fatal("an unknown output format must be rejected")
	}
	var u *UsageError
	if !errors.As(err, &u) {
		t.Errorf("want UsageError, got %T: %v", err, err)
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit code = %d, want %d", ExitCode(err), ExitUsage)
	}
}

func TestRetiredAPIHost_IsRefusedAsUsage(t *testing.T) {
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", "https://swiftcloud.co.zm/api/v1")
	_, err := run(t, "completion", "bash")
	if err == nil {
		t.Fatal("a retired API host must be refused before any command runs")
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit code = %d, want %d (usage)", ExitCode(err), ExitUsage)
	}
	if !strings.Contains(err.Error(), "cloud.co.zm/api/v1") {
		t.Errorf("error should name the replacement URL: %v", err)
	}
}

func TestDefaultsResolveWithNoConfigFile(t *testing.T) {
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	if _, err := run(t, "completion", "bash"); err != nil {
		t.Fatalf("a fresh machine with no config must work: %v", err)
	}
	if cfg == nil || cfg.APIURL != "https://cloud.co.zm/api/v1" {
		t.Errorf("resolved API URL = %v, want production default", cfg)
	}
}
