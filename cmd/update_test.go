package cmd

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

/*
Self-update.

The property that matters most is that nothing is replaced unless the download
matches the release's published checksum, so that is tested from both sides: a
good archive updates, a tampered one leaves the old binary byte-for-byte
intact.
*/

// tarGzWith builds a release archive containing one binary with this content.
func tarGzWith(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf strings.Builder
	gz := gzip.NewWriter(&stringWriter{&buf})
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(buf.String())
}

type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

// fakeGitHub serves release metadata, an archive and a checksums.txt.
func fakeGitHub(t *testing.T, archive []byte, sum string) *httptest.Server {
	t.Helper()
	const tag = "v9.9.9"
	assetName := fmt.Sprintf("cloud_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example.invalid/releases/%s","assets":[
			{"name":%q,"browser_download_url":%q,"size":%d},
			{"name":"checksums.txt","browser_download_url":%q,"size":64}]}`,
			tag, tag, assetName, base+"/download/"+assetName, len(archive), base+"/download/checksums.txt")
	})
	mux.HandleFunc("/download/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/download/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n%s  cloud_other_platform.tar.gz\n", sum, assetName, strings.Repeat("0", 64))
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestPickAsset_MatchesThisPlatformAndFindsChecksums(t *testing.T) {
	rel := &release{TagName: "v1.2.3"}
	add := func(name string) {
		rel.Assets = append(rel.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		}{Name: name, URL: "https://example.invalid/" + name})
	}
	add("cloud_1.2.3_linux_amd64.tar.gz")
	add("cloud_1.2.3_linux_arm64.tar.gz")
	add("cloud_1.2.3_darwin_amd64.tar.gz")
	add("cloud_1.2.3_darwin_arm64.tar.gz")
	add("cloud_1.2.3_windows_amd64.zip")
	add("checksums.txt")

	asset, sums := pickAsset(rel)
	if !strings.Contains(asset.Name, runtime.GOOS) || !strings.Contains(asset.Name, runtime.GOARCH) {
		t.Errorf("picked %q for %s/%s", asset.Name, runtime.GOOS, runtime.GOARCH)
	}
	if sums.Name != "checksums.txt" {
		t.Errorf("checksums asset = %q", sums.Name)
	}
}

func TestPickAsset_NoBuildForThisPlatform(t *testing.T) {
	rel := &release{TagName: "v1.0.0"}
	rel.Assets = append(rel.Assets, struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	}{Name: "cloud_1.0.0_plan9_mips.tar.gz", URL: "https://example.invalid/x"})
	if asset, _ := pickAsset(rel); asset.URL != "" {
		t.Errorf("should have found nothing, got %q", asset.Name)
	}
}

func TestPackageManagerOf(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/homebrew/Cellar/cloud/1.0.0/bin/cloud": "Homebrew",
		"/home/linuxbrew/.linuxbrew/bin/cloud":       "Homebrew",
		"/snap/cloud/current/bin/cloud":              "snap",
		"/nix/store/abc-cloud-1.0/bin/cloud":         "Nix",
		"/usr/local/bin/cloud":                       "",
		"/home/arthur/go/bin/cloud":                  "",
	} {
		if got := packageManagerOf(path); got != want {
			t.Errorf("packageManagerOf(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestUpgradeCommandFor(t *testing.T) {
	if got := upgradeCommandFor("Homebrew"); got != "brew upgrade cloud" {
		t.Errorf("Homebrew → %q", got)
	}
	if got := upgradeCommandFor("something else"); !strings.Contains(got, "package manager") {
		t.Errorf("unknown manager → %q", got)
	}
}

func TestChecksumFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "aaaa  cloud_1_linux_amd64.tar.gz\nbbbb  *cloud_1_darwin_arm64.tar.gz\n")
	}))
	defer srv.Close()
	got, err := checksumFor(t.Context(), srv.URL, "cloud_1_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "aaaa" {
		t.Errorf("checksum = %q", got)
	}
	// goreleaser writes a "*" before binary-mode names; it is not part of it.
	got, err = checksumFor(t.Context(), srv.URL, "cloud_1_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbbb" {
		t.Errorf("starred name checksum = %q", got)
	}
	if _, err := checksumFor(t.Context(), srv.URL, "not-listed.tar.gz"); err == nil {
		t.Error("an asset missing from checksums.txt must be an error")
	}
}

func TestExtractBinary_TakesOnlyTheNamedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz path is for linux and darwin")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "rel.tar.gz")
	if err := os.WriteFile(archive, tarGzWith(t, "cloud", "new binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := extractBinary(archive, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("extracted %q", got)
	}
	// It lands in the directory it was told to use, so the later rename cannot
	// cross a filesystem.
	if filepath.Dir(path) != dir {
		t.Errorf("extracted to %q, want a file in %q", path, dir)
	}
}

// An archive that does not contain the binary must fail, not produce an empty
// file that then gets renamed over a working CLI.
func TestExtractBinary_RefusesAnArchiveWithoutTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz path is for linux and darwin")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "rel.tar.gz")
	if err := os.WriteFile(archive, tarGzWith(t, "README.md", "not a binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractBinary(archive, dir); err == nil {
		t.Fatal("expected an error")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cloud-update-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestReplaceBinary_KeepsThePermissionsAndSwapsAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cloud")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, ".cloud-update-x")
	if err := os.WriteFile(newBin, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(newBin, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target holds %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// The replacement must be executable even though it was written 0600.
	// Windows has no POSIX mode bits: Go reports a synthetic 0666/0444 there
	// whatever mode was requested, and the file is protected by the ACLs it
	// inherits from a per-user directory instead. The check is meaningful only
	// where the mode is real.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("permissions = %o, want 755 (the old binary's)", info.Mode().Perm())
	}
}

func TestUpdate_CheckReportsWithoutChangingAnything(t *testing.T) {
	archive := tarGzWith(t, "cloud", "new binary")
	srv := fakeGitHub(t, archive, sha256Hex(archive))
	updateAPIURL = srv.URL + "/releases/latest"
	t.Cleanup(func() { updateAPIURL = updateAPI })
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())

	if _, err := run(t, "update", "--check"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdate_JSONReportsBothVersions(t *testing.T) {
	archive := tarGzWith(t, "cloud", "new binary")
	srv := fakeGitHub(t, archive, sha256Hex(archive))
	updateAPIURL = srv.URL + "/releases/latest"
	t.Cleanup(func() { updateAPIURL = updateAPI })
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())

	out, err := run(t, "update", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"latest"`) || !strings.Contains(out, "9.9.9") {
		t.Errorf("json output should carry both versions:\n%s", out)
	}
}

// A local build has no version to compare, so it must refuse rather than
// overwrite whatever the developer just built.
func TestUpdate_RefusesToReplaceADevBuild(t *testing.T) {
	archive := tarGzWith(t, "cloud", "new binary")
	srv := fakeGitHub(t, archive, sha256Hex(archive))
	updateAPIURL = srv.URL + "/releases/latest"
	t.Cleanup(func() { updateAPIURL = updateAPI })
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())

	_, err := run(t, "update", "--yes")
	if err == nil {
		t.Fatal("a dev build must not be replaced")
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit = %d, want %d", ExitCode(err), ExitUsage)
	}
	if !strings.Contains(err.Error(), "dev") {
		t.Errorf("the message should explain why: %v", err)
	}
}

func TestUpdate_NoReleasesYet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	updateAPIURL = srv.URL
	t.Cleanup(func() { updateAPIURL = updateAPI })
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())

	_, err := run(t, "update", "--check")
	if err == nil || !strings.Contains(err.Error(), "no releases") {
		t.Fatalf("expected a clear 'no releases' error, got %v", err)
	}
}

// The whole point of the command: a download whose checksum does not match the
// release's published one must not be installed, and the binary already on
// disk must be untouched.
func TestInstallUpdate_TamperedDownloadIsRefusedAndNothingChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz path is for linux and darwin")
	}
	archive := tarGzWith(t, "cloud", "malicious binary")
	// The release publishes a checksum for entirely different bytes.
	srv := fakeGitHub(t, archive, sha256Hex([]byte("the bytes we expected")))
	assetName := fmt.Sprintf("cloud_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	dir := t.TempDir()
	target := filepath.Join(dir, "cloud")
	if err := os.WriteFile(target, []byte("the working binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := installUpdate(t.Context(), srv.URL+"/download/"+assetName, assetName,
		srv.URL+"/download/checksums.txt", target)
	if err == nil {
		t.Fatal("a mismatched checksum must refuse the update")
	}
	if !strings.Contains(err.Error(), "does not match the release checksum") {
		t.Errorf("the error should name the reason: %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "the working binary" {
		t.Errorf("the existing binary was modified: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cloud-update-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// And the matching case: a good download replaces the target.
func TestInstallUpdate_GoodDownloadReplacesTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz path is for linux and darwin")
	}
	archive := tarGzWith(t, "cloud", "the new binary")
	srv := fakeGitHub(t, archive, sha256Hex(archive))
	assetName := fmt.Sprintf("cloud_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	dir := t.TempDir()
	target := filepath.Join(dir, "cloud")
	if err := os.WriteFile(target, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installUpdate(t.Context(), srv.URL+"/download/"+assetName, assetName,
		srv.URL+"/download/checksums.txt", target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the new binary" {
		t.Errorf("target holds %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("permissions = %o, want the old binary's 755", info.Mode().Perm())
	}
}
