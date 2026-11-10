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

	"cloud/internal/version"
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
	assetName := archiveName(strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
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

func TestArchiveName_MatchesGoreleaserNameTemplate(t *testing.T) {
	for _, c := range []struct{ ver, goos, goarch, want string }{
		{"1.2.3", "linux", "amd64", "cloud_1.2.3_linux_amd64.tar.gz"},
		{"1.2.3", "darwin", "arm64", "cloud_1.2.3_darwin_arm64.tar.gz"},
		{"1.2.3", "windows", "amd64", "cloud_1.2.3_windows_amd64.zip"},
		{"1.2.3-rc.1", "windows", "arm64", "cloud_1.2.3-rc.1_windows_arm64.zip"},
	} {
		if got := archiveName(c.ver, c.goos, c.goarch); got != c.want {
			t.Errorf("archiveName(%q, %q, %q) = %q, want %q", c.ver, c.goos, c.goarch, got, c.want)
		}
	}
}

// releaseWith builds release metadata listing these asset names.
func releaseWith(tag string, names ...string) *release {
	rel := &release{TagName: tag}
	for _, name := range names {
		rel.Assets = append(rel.Assets, releaseAsset{Name: name, URL: "https://example.invalid/" + name})
	}
	return rel
}

func TestPickAsset_MatchesEachPlatformAndFindsChecksums(t *testing.T) {
	rel := releaseWith("v1.2.3",
		"cloud_1.2.3_linux_amd64.tar.gz",
		"cloud_1.2.3_linux_arm64.tar.gz",
		"cloud_1.2.3_darwin_amd64.tar.gz",
		"cloud_1.2.3_darwin_arm64.tar.gz",
		"cloud_1.2.3_windows_amd64.zip",
		"cloud_1.2.3_windows_arm64.zip",
		"checksums.txt",
	)
	for _, p := range [][2]string{
		{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"},
		{"darwin", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"},
	} {
		asset, sums := pickAsset(rel, p[0], p[1])
		if want := archiveName("1.2.3", p[0], p[1]); asset.Name != want {
			t.Errorf("%s/%s: picked %q, want %q", p[0], p[1], asset.Name, want)
		}
		if sums.Name != "checksums.txt" {
			t.Errorf("checksums asset = %q", sums.Name)
		}
	}
}

// A release also carries an SBOM and signatures whose names contain the
// archive's. Matching by substring would pick one of those, whichever came
// last; only the exact archive name is acceptable.
func TestPickAsset_IgnoresSBOMsAndSignatures(t *testing.T) {
	for _, order := range [][]string{
		{"cloud_1.2.3_linux_amd64.tar.gz", "cloud_1.2.3_linux_amd64.tar.gz.sbom.json", "cloud_1.2.3_linux_amd64.tar.gz.sigstore.json"},
		{"cloud_1.2.3_linux_amd64.tar.gz.sbom.json", "cloud_1.2.3_linux_amd64.tar.gz.sigstore.json", "cloud_1.2.3_linux_amd64.tar.gz"},
	} {
		names := append([]string{"checksums.txt", "checksums.txt.sigstore.json"}, order...)
		asset, sums := pickAsset(releaseWith("v1.2.3", names...), "linux", "amd64")
		if asset.Name != "cloud_1.2.3_linux_amd64.tar.gz" {
			t.Errorf("picked %q from %v", asset.Name, names)
		}
		if sums.Name != "checksums.txt" {
			t.Errorf("checksums asset = %q", sums.Name)
		}
	}
}

func TestPickAsset_NoBuildForThisPlatform(t *testing.T) {
	rel := releaseWith("v1.0.0", "cloud_1.0.0_plan9_mips.tar.gz", "cloud_1.0.0_linux_amd64.tar.gz.sbom.json")
	if asset, _ := pickAsset(rel, "linux", "amd64"); asset.URL != "" {
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

	err := installUpdate(t.Context(), releaseAsset{Name: assetName, URL: srv.URL + "/download/" + assetName, Size: int64(len(archive))},
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
	if err := installUpdate(t.Context(), releaseAsset{Name: assetName, URL: srv.URL + "/download/" + assetName, Size: int64(len(archive))},
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

// ── download size ────────────────────────────────────────────────────────────

func TestDownload_AcceptsExactlyTheListedSize(t *testing.T) {
	body := strings.Repeat("x", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	path, err := download(t.Context(), srv.URL, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(body))
	}
}

// A server announcing more than the release lists is refused before a byte is
// written.
func TestDownload_RefusesAContentLengthLargerThanListed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-length", "5000")
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	t.Cleanup(srv.Close)
	if path, err := download(t.Context(), srv.URL, 1000); err == nil {
		_ = os.Remove(path)
		t.Fatal("a download larger than the listed size must be refused")
	}
}

// And one that announces nothing (chunked) but sends too much is cut off and
// refused, leaving no temporary file behind.
func TestDownload_RefusesMoreBytesThanListedWithoutContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 10 {
			_, _ = w.Write([]byte(strings.Repeat("x", 500)))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	path, err := download(t.Context(), srv.URL, 1000)
	if err == nil {
		_ = os.Remove(path)
		t.Fatal("a download larger than the listed size must be refused")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Errorf("the error should say why: %v", err)
	}
	if runtime.GOOS != "windows" {
		entries, _ := os.ReadDir(tmp)
		if len(entries) != 0 {
			t.Errorf("a partial download was left behind: %v", entries)
		}
	}
}

// With no size in the metadata the hard cap still applies; nothing near it is
// sent here, so this just shows a zero size does not mean "zero bytes".
func TestDownload_UnlistedSizeFallsBackToTheCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "some bytes")
	}))
	t.Cleanup(srv.Close)
	path, err := download(t.Context(), srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
}

// ── Windows replacement ─────────────────────────────────────────────────────

// windowsRename imitates Windows: a running executable can be renamed away,
// but nothing can be renamed onto it while it is still there.
func windowsRename(running string) func(string, string) error {
	return func(from, to string) error {
		if to == running {
			if _, err := os.Stat(running); err == nil {
				return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
			}
		}
		return os.Rename(from, to)
	}
}

func TestReplaceBinaryOn_WindowsMovesTheRunningBinaryAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cloud.exe")
	newBin := filepath.Join(dir, ".cloud-update-x")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBin, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A leftover from a previous update must not get in the way.
	if err := os.WriteFile(target+".old", []byte("older"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinaryOn("windows", windowsRename(target), newBin, target); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Errorf("target holds %q, want the new binary", got)
	}
	if got, _ := os.ReadFile(target + ".old"); string(got) != "old" {
		t.Errorf("%s.old holds %q, want the previous binary", target, got)
	}
}

// The plain rename that works on Linux and macOS is exactly what Windows
// refuses; it must not be what the Windows path does.
func TestReplaceBinaryOn_WindowsRenameOverRunningBinaryIsRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cloud.exe")
	newBin := filepath.Join(dir, ".cloud-update-x")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	_ = os.WriteFile(newBin, []byte("new"), 0o600)
	if err := replaceBinaryOn("linux", windowsRename(target), newBin, target); err == nil {
		t.Fatal("the fake should refuse a rename onto the running binary")
	}
}

// If the new binary cannot be moved in, the old one goes back where it was.
func TestReplaceBinaryOn_WindowsRollsBackWhenTheSecondRenameFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cloud.exe")
	newBin := filepath.Join(dir, ".cloud-update-x")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	_ = os.WriteFile(newBin, []byte("new"), 0o600)

	failing := func(from, to string) error {
		if from == newBin {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
		}
		return os.Rename(from, to)
	}
	err := replaceBinaryOn("windows", failing, newBin, target)
	if err == nil {
		t.Fatal("expected the failed rename to be reported")
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("target holds %q, want the old binary restored", got)
	}
	if _, statErr := os.Stat(target + ".old"); !os.IsNotExist(statErr) {
		t.Errorf("%s.old should be gone after the rollback", target)
	}
	// sudo means nothing on Windows; the advice must fit the platform.
	if strings.Contains(err.Error(), "sudo") || !strings.Contains(err.Error(), "cloud.exe") {
		t.Errorf("the Windows error should give Windows advice: %v", err)
	}
}

func TestReplaceBinaryOn_WindowsCannotMoveTheBinaryAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cloud.exe")
	newBin := filepath.Join(dir, ".cloud-update-x")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	_ = os.WriteFile(newBin, []byte("new"), 0o600)

	denied := func(from, to string) error {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
	}
	err := replaceBinaryOn("windows", denied, newBin, target)
	if err == nil || strings.Contains(err.Error(), "sudo") || !strings.Contains(err.Error(), "install.ps1") {
		t.Fatalf("want a Windows-specific permission error, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("target holds %q, want it untouched", got)
	}
}

func TestReplaceBinary_UnixPermissionErrorSuggestsSudo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cloud")
	newBin := filepath.Join(dir, ".cloud-update-x")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	_ = os.WriteFile(newBin, []byte("new"), 0o600)
	denied := func(from, to string) error {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
	}
	err := replaceBinaryOn("linux", denied, newBin, target)
	if err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("want the sudo advice, got %v", err)
	}
}

func TestRemoveStaleBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cloud.exe")
	if err := os.WriteFile(target+".old", []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	removeStaleBinary(target)
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Error("the stale binary should have been removed")
	}
	removeStaleBinary(target) // nothing there: no panic, no error to report
}

// ── version comparison ──────────────────────────────────────────────────────

// setVersion makes the running CLI report v for the rest of the test.
func setVersion(t *testing.T, v string) {
	t.Helper()
	version.Set(v, "none", "unknown")
	t.Cleanup(func() { version.Set("dev", "none", "unknown") })
}

// updateAgainst points `cloud update` at a fake release whose checksum never
// matches, so that even a regression which went ahead with the update could
// not replace the test binary.
func updateAgainst(t *testing.T) {
	t.Helper()
	archive := tarGzWith(t, "cloud", "new binary")
	srv := fakeGitHub(t, archive, sha256Hex([]byte("never matches")))
	updateAPIURL = srv.URL + "/releases/latest"
	t.Cleanup(func() { updateAPIURL = updateAPI })
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
}

func TestUpdate_SameVersionIsUpToDate(t *testing.T) {
	updateAgainst(t)
	setVersion(t, "9.9.9")
	_, stderr, err := runWithStderr(t, "update", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "is the latest release") {
		t.Errorf("stderr = %q", stderr)
	}
}

// A version string compared as text says 10.0.0 != 9.9.9 and "updates" —
// that is, downgrades. Compared as semver it is newer and nothing happens.
func TestUpdate_NeverDowngrades(t *testing.T) {
	updateAgainst(t)
	setVersion(t, "10.0.0")
	_, stderr, err := runWithStderr(t, "update", "--yes")
	if err != nil {
		t.Fatalf("a newer local version is not an error: %v", err)
	}
	if !strings.Contains(stderr, "newer than the latest release") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestUpdate_OlderReleaseVersionIsOfferedTheUpdate(t *testing.T) {
	updateAgainst(t)
	setVersion(t, "9.9.8")
	_, stderr, err := runWithStderr(t, "update", "--check")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "9.9.9 is available") {
		t.Errorf("stderr = %q", stderr)
	}
}

// Builds that are not clean releases are refused like "dev": a dirty tree,
// or a build some commits past a tag, has no place in the release order.
func TestUpdate_RefusesToReplaceANonReleaseBuild(t *testing.T) {
	for _, v := range []string{"0.3.4-dirty", "0.3.4-3-g5ed1293", "v0.3.4-3-g5ed1293-dirty", "5ed1293", ""} {
		t.Run(v, func(t *testing.T) {
			updateAgainst(t)
			setVersion(t, v)
			_, err := run(t, "update", "--yes")
			if err == nil {
				t.Fatalf("version %q must not be replaced", v)
			}
			if ExitCode(err) != ExitUsage {
				t.Errorf("exit = %d, want %d", ExitCode(err), ExitUsage)
			}
			if !strings.Contains(err.Error(), "local build") {
				t.Errorf("the message should explain why: %v", err)
			}
		})
	}
}

func TestUpdate_RefusesALatestTagThatIsNotAVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"nightly","html_url":"https://example.invalid","assets":[]}`)
	}))
	t.Cleanup(srv.Close)
	updateAPIURL = srv.URL
	t.Cleanup(func() { updateAPIURL = updateAPI })
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	setVersion(t, "1.0.0")

	_, err := run(t, "update", "--check")
	if err == nil || !strings.Contains(err.Error(), "not a release version") {
		t.Fatalf("want a refusal of the tag, got %v", err)
	}
}
