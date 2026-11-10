package s3

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

/*
A downloaded key is untrusted input to the filesystem. These cases are the
ways a key can try to leave the directory it is downloaded into.
*/

func TestSafeLocalPath_AcceptsOrdinaryKeys(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"a.txt", "dir/b.txt", "deep/er/c.txt", "..dots..", "a..b/c", ".hidden"} {
		got, err := safeLocalPath(dir, rel, false)
		if err != nil {
			t.Errorf("%q should be accepted: %v", rel, err)
			continue
		}
		if want := filepath.Join(dir, filepath.FromSlash(rel)); got != want {
			t.Errorf("%q → %q, want %q", rel, got, want)
		}
	}
}

func TestSafeLocalPath_RefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{
		"",
		"../x",
		"..",
		"site/../../.ssh/authorized_keys",
		"a/../../b",
		"/etc/passwd",
		`\Windows\x`,
		`site/..\..\x`, // Windows reads "\" as a separator
		`..\x`,
		"a/./..",
		".",
		"a/\x00b",
	} {
		if p, err := safeLocalPath(dir, rel, false); err == nil {
			t.Errorf("%q must be refused, got %q", rel, p)
		} else if !errors.Is(err, ErrUnsafeKey) {
			t.Errorf("%q: error should be ErrUnsafeKey, got %v", rel, err)
		}
	}
}

// On Windows a backslash is a separator and a colon names a drive or an
// alternate data stream, so neither may appear in a key at all there.
func TestSafeLocalPath_WindowsRules(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{`a\b.txt`, `C:x`, `c:/Windows/x`, `file.txt:stream`, `dir/a\..\..\x`} {
		if p, err := safeLocalPath(dir, rel, true); err == nil {
			t.Errorf("%q must be refused on Windows, got %q", rel, p)
		}
	}
	if _, err := safeLocalPath(dir, "dir/ok.txt", true); err != nil {
		t.Errorf("a plain key must be fine on Windows too: %v", err)
	}
}

func TestPlanDownload_SkipsAndFlagsKeysThatEscape(t *testing.T) {
	dir := t.TempDir()
	remote := []Object{
		{Key: "site/../../.ssh/authorized_keys", Size: 5},
		{Key: `site/..\..\x`, Size: 5},
		{Key: "site/ok.txt", Size: 3},
	}
	actions, err := PlanDownload(remote, dir, "site", SyncOptions{})
	if err != nil {
		t.Fatalf("one bad key must not fail the plan: %v", err)
	}
	var transfers, unsafe int
	for _, a := range actions {
		switch {
		case a.Kind == Transfer:
			transfers++
			if a.Key != "site/ok.txt" || a.Root != dir || a.Rel != "ok.txt" {
				t.Errorf("unexpected transfer: %+v", a)
			}
		case a.Unsafe:
			unsafe++
			if a.Kind != Skip || a.LocalPath != "" {
				t.Errorf("an unsafe key must be a skip with no local path: %+v", a)
			}
		}
	}
	if transfers != 1 || unsafe != 2 {
		t.Errorf("transfers=%d unsafe=%d, want 1 and 2: %+v", transfers, unsafe, actions)
	}
}

// "site" is a directory for a recursive download: site.html and site2/… are
// different objects that happen to share the letters.
func TestPlanDownload_PrefixIsADirectory(t *testing.T) {
	dir := t.TempDir()
	remote := []Object{
		{Key: "site", Size: 1},
		{Key: "site.html", Size: 1},
		{Key: "site2/a.txt", Size: 1},
		{Key: "site/index.html", Size: 1},
	}
	actions, err := PlanDownload(remote, dir, "site", SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Key != "site/index.html" {
		t.Fatalf("want only site/index.html, got %+v", actions)
	}
	if filepath.Base(actions[0].LocalPath) != "index.html" {
		t.Errorf("local path = %q", actions[0].LocalPath)
	}
}

func TestDirPrefix(t *testing.T) {
	for in, want := range map[string]string{"": "", "site": "site/", "site/": "site/", "a/b": "a/b/"} {
		if got := DirPrefix(in); got != want {
			t.Errorf("DirPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// The plan is checked by name, but a symlink already inside the target
// directory could still carry a write elsewhere. RunDownload writes through
// an os.Root, which refuses that.
func TestRunDownload_DoesNotFollowASymlinkOutOfTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		_, _ = w.Write([]byte("payload"))
		return true
	})
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	err := c.RunDownload(context.Background(), "demo", []Action{
		{Kind: Transfer, Key: "link/x.txt", LocalPath: filepath.Join(dir, "link", "x.txt"), Root: dir, Rel: filepath.Join("link", "x.txt")},
	}, nil)
	if err == nil {
		t.Fatal("a write through a symlink out of the root must fail")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("something was written outside the root: %v", entries)
	}
}

func TestRunDownload_WritesUnderTheRoot(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		_, _ = w.Write([]byte("payload"))
		return true
	})
	dir := filepath.Join(t.TempDir(), "not-yet")
	rel := filepath.Join("a", "b.txt")
	if err := c.RunDownload(context.Background(), "demo", []Action{
		{Kind: Transfer, Key: "p/a/b.txt", LocalPath: filepath.Join(dir, rel), Root: dir, Rel: rel},
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil || string(got) != "payload" {
		t.Fatalf("file = %q, %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "a"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cloud-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

// `sync ./link s3://…` where ./link points at a directory must upload that
// directory, not walk the link and find nothing.
func TestWalkDir_FollowsASymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link inside the tree stays unfollowed.
	if err := os.Symlink(t.TempDir(), filepath.Join(target, "inner-link")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	files, err := WalkDir(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Rel != "a.txt" {
		t.Errorf("walk of a symlinked root = %+v", files)
	}
}
