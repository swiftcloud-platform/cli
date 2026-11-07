package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// One API, many spellings, one credential slot: otherwise a login through
// one spelling and a logout through another leaves the token behind.
func TestHostKey_NormalisesSpellingsOfOneHost(t *testing.T) {
	want, _ := HostKey("https://cloud.co.zm/api/v1")
	for _, u := range []string{
		"https://Cloud.co.zm/api/v1",
		"https://cloud.co.zm./api/v1",
		"https://cloud.co.zm:443/api/v1",
		"https://CLOUD.CO.ZM.:443/x",
	} {
		got, err := HostKey(u)
		if err != nil || got != want {
			t.Errorf("HostKey(%s) = %q %v, want %q", u, got, err, want)
		}
	}
	if k, _ := HostKey("http://localhost:80/api/v1"); k != "localhost" {
		t.Errorf("default http port must be dropped: %q", k)
	}
	if k, _ := HostKey("https://cloud.co.zm:8443/api/v1"); k != "cloud.co.zm_8443" {
		t.Errorf("a non-default port must be kept: %q", k)
	}
	// Existing files for these keep working.
	if k, _ := HostKey("http://localhost:5173/api/v1"); k != "localhost_5173" {
		t.Errorf("localhost key changed: %q", k)
	}
	if k, _ := HostKey("http://[::1]:5173/api/v1"); k != "[__1]_5173" {
		t.Errorf("IPv6 key changed: %q", k)
	}
}

func TestStore_SaveLeavesNoTempFileAndReplaces(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(nil)}
	api := "https://cloud.co.zm/api/v1"
	for _, tok := range []string{"one", "two"} {
		if err := s.Save(api, &Credential{Token: tok, Kind: KindSession}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "tokens"))
	if len(entries) != 1 || entries[0].Name() != "cloud.co.zm.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("tokens dir holds %v, want only cloud.co.zm.json", names)
	}
	c, err := s.Load(api)
	if err != nil || c.Token != "two" {
		t.Errorf("got %+v %v", c, err)
	}
}

// A planted file at the old fixed temp name must not be written through.
func TestStore_SaveDoesNotFollowAPlantedTempSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	s := &Store{Dir: t.TempDir(), Env: env(nil)}
	api := "https://cloud.co.zm/api/v1"
	p, _ := s.path(api)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	victim := filepath.Join(t.TempDir(), "victim")
	_ = os.WriteFile(victim, []byte("untouched"), 0o600)
	_ = os.Symlink(victim, p+".tmp")
	if err := s.Save(api, &Credential{Token: "secret", Kind: KindSession}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "untouched" {
		t.Errorf("the token was written through a planted symlink: %q", b)
	}
}

func TestStore_TightensLooseDirectoriesAndWarnsOnALooseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX modes on Windows")
	}
	dir := t.TempDir()
	var warnings []string
	s := &Store{Dir: dir, Env: env(nil), Warn: func(m string) { warnings = append(warnings, m) }}
	api := "https://cloud.co.zm/api/v1"
	if err := s.Save(api, &Credential{Token: "x", Kind: KindSession}); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(dir, 0o755)
	_ = os.Chmod(filepath.Join(dir, "tokens"), 0o777)
	p, _ := s.path(api)
	_ = os.Chmod(p, 0o644)

	if _, err := s.Load(api); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, "tokens")} {
		st, _ := os.Stat(d)
		if st.Mode().Perm() != 0o700 {
			t.Errorf("%s left at %o, want 0700", d, st.Mode().Perm())
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "0600") {
		t.Errorf("want one warning about the file mode, got %q", warnings)
	}
	for _, w := range warnings {
		if strings.Contains(w, `"x"`) {
			t.Error("a warning must never contain the token")
		}
	}
}

func TestStore_ClearAllAndStored(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(map[string]string{"CLOUD_TOKEN": "from-env"})}
	_ = s.Save("https://cloud.co.zm/api/v1", &Credential{Token: "a", Kind: KindSession})
	_ = s.Save("http://localhost:5173/api/v1", &Credential{Token: "b", Kind: KindSession})

	c, err := s.Stored("https://cloud.co.zm/api/v1")
	if err != nil || c.Token != "a" {
		t.Errorf("Stored must read the file, not CLOUD_TOKEN: %+v %v", c, err)
	}
	removed, err := s.ClearAll()
	if err != nil || len(removed) != 2 {
		t.Fatalf("ClearAll = %v, %v", removed, err)
	}
	if len(s.Hosts()) != 0 {
		t.Errorf("credentials left: %v", s.Hosts())
	}
	if removed, err := (&Store{Dir: t.TempDir()}).ClearAll(); err != nil || removed != nil {
		t.Errorf("ClearAll on an empty store = %v, %v", removed, err)
	}
}
