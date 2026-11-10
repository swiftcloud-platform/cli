package cmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"cloud/internal/version"
)

/*
Self-update.

The rules this follows, and why:

  - Nothing is replaced until the download's SHA-256 matches the release's own
    checksums.txt. That catches an interrupted or corrupted download, and one
    swapped in transit. It does NOT protect against a compromised release:
    checksums.txt comes from the same GitHub release as the archive, so
    whoever can replace one can replace the other. Releases are signed
    (keyless cosign, checksums.txt.sigstore.json) from the first release after
    v0.3.4, but this command does not verify that signature yet — see
    "Verifying a release" in the README for doing it by hand.
  - Only a strictly newer release is installed. Versions are compared as
    semver, so a downgrade never happens automatically, and a build that is
    not itself a clean release ("dev", "-dirty", git describe's "-N-g<hash>")
    is never replaced, because it has no place in the release order.
  - The download is capped at the size the release lists for the asset, so a
    hostile or broken server cannot fill the disk.
  - The new binary is written beside the old one and renamed into place, so the
    swap is atomic on the same filesystem and a failure leaves the working
    binary exactly where it was.
  - A CLI installed by a package manager is not updated in place. Homebrew
    tracks its own files, and overwriting them silently would break `brew
    upgrade` later; the command says which upgrade to run instead.
  - "dev" builds are refused, because there is no version to compare and
    overwriting someone's locally built binary is never what they meant.
  - On Windows a running .exe cannot be overwritten, but it can be renamed.
    The old binary is moved aside to cloud.exe.old, the new one moved in, and
    the first move undone if the second fails. The .old file is removed by the
    next `cloud update`, once it is no longer the running image.
*/

const (
	// updateRepo is where release files are hosted. Users never see it:
	// everything the CLI prints points at downloadPage instead.
	updateRepo = "swiftcloud-platform/cli"
	// updateAPI is the release metadata. Overridden in tests.
	updateAPI = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	// downloadPage is the one address `cloud update` shows people: the
	// install instructions on cloud.co.zm.
	downloadPage = "https://cloud.co.zm/docs/cli#install"
)

var (
	updateCheckOnly bool
	updateYes       bool
	// updateAPIURL is the metadata endpoint, injectable for tests.
	updateAPIURL = updateAPI
)

// release is the part of GitHub's release JSON this needs.
type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// releaseAsset is one file attached to a release.
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update this CLI to the latest release",
	Long: `Download the latest release and replace this binary with it.

The download's checksum is verified against the release's own checksums.txt
before anything is replaced, and the new binary is renamed into place, so a
failed update leaves the working one untouched. Only a newer release is
installed: this never downgrades, and never replaces a local build.

The checksum guards against a broken or swapped download, not against a
compromised release; the release's signature is not checked here yet. To
install by hand instead, see ` + downloadPage + `.

If the CLI came from Homebrew, this reports the command to use instead rather
than overwriting files a package manager owns.`,
	Example: `  cloud update
  cloud update --check          # say what is available, change nothing
  cloud update --yes            # no confirmation, for automation`,
	Args: cobra.NoArgs,
	// No credential needed, and it must work on a machine that has never
	// signed in — but the config still resolves, for --output.
	RunE: func(cmd *cobra.Command, _ []string) error {
		errOut := cmd.ErrOrStderr()
		current := version.Version()

		self, err := os.Executable()
		if err != nil {
			return err
		}
		self, err = filepath.EvalSymlinks(self)
		if err != nil {
			return err
		}

		// What a previous update on Windows had to leave behind.
		removeStaleBinary(self)

		rel, err := latestRelease(cmd.Context())
		if err != nil {
			return err
		}
		if rel.TagName == "" {
			return errors.New("the latest release has no version tag")
		}
		latestV, ok := version.Release(rel.TagName)
		if !ok {
			return fmt.Errorf("the latest release is tagged %q, which is not a release version", rel.TagName)
		}
		latest := strings.TrimPrefix(latestV, "v")
		currentV, isRelease := version.Release(current)

		if printer.Format != "table" {
			return printer.Print(map[string]string{
				"current":   current,
				"latest":    latest,
				"url":       downloadPage,
				"installed": self,
			})
		}

		if isRelease {
			switch c := semver.Compare(latestV, currentV); {
			case c == 0:
				fmt.Fprintf(errOut, "cloud %s is the latest release.\n", current)
				return nil
			case c < 0:
				// Never a downgrade, whatever the latest release says.
				fmt.Fprintf(errOut, "cloud %s is newer than the latest release (%s); nothing to do.\n", current, latest)
				return nil
			}
			fmt.Fprintf(errOut, "cloud %s is available (you have %s)\n%s\n", latest, current, downloadPage)
		} else {
			fmt.Fprintf(errOut, "The latest release is cloud %s (this is a local build, %q)\n%s\n", latest, current, downloadPage)
		}
		if updateCheckOnly {
			return nil
		}

		// A package manager's files are not ours to replace.
		if manager := packageManagerOf(self); manager != "" {
			return &UsageError{fmt.Errorf("this CLI was installed with %s, which manages its own files — run `%s` instead",
				manager, upgradeCommandFor(manager))}
		}
		if !isRelease {
			return &UsageError{fmt.Errorf("this is a local build (version %q), not a release, so there is nothing to compare and it is not replaced — install a release with the install script, or build again from source", current)}
		}

		asset, sums := pickAsset(rel, runtime.GOOS, runtime.GOARCH)
		if asset.URL == "" {
			return fmt.Errorf("release %s has no build for %s/%s", rel.TagName, runtime.GOOS, runtime.GOARCH)
		}
		if sums.URL == "" {
			return fmt.Errorf("release %s publishes no checksums.txt, so the download cannot be verified", rel.TagName)
		}

		if !updateYes {
			if err := confirmUpdate(cmd, latest); err != nil {
				return err
			}
		}

		fmt.Fprintf(errOut, "Downloading %s…\n", asset.Name)
		if err := installUpdate(cmd.Context(), asset, sums.URL, self); err != nil {
			return err
		}
		fmt.Fprintf(errOut, "Updated to cloud %s at %s\n", latest, self)
		return nil
	},
}

// installUpdate downloads an asset, refuses it unless its SHA-256 matches the
// release's published checksum, and only then replaces target.
//
// Every failure path leaves target exactly as it was — that is the whole
// contract, and it is why the checksum is checked before extraction rather
// than after.
func installUpdate(ctx context.Context, asset releaseAsset, sumsURL, target string) error {
	archive, err := download(ctx, asset.URL, asset.Size)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archive) }()

	want, err := checksumFor(ctx, sumsURL, asset.Name)
	if err != nil {
		return err
	}
	got, err := fileSHA256(archive)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("the download does not match the release checksum — nothing was changed (expected %s, got %s)", want, got)
	}

	binary, err := extractBinary(archive, filepath.Dir(target))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(binary) }()

	return replaceBinary(binary, target)
}

// latestRelease fetches the release metadata.
func latestRelease(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/vnd.github+json")
	req.Header.Set("user-agent", version.UserAgent())
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the release server to look for a release: %w", hideURL(err))
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNotFound {
		return nil, errors.New("no releases have been published yet")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the release server answered HTTP %d looking for the latest release", res.StatusCode)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("could not read the release metadata: %w", err)
	}
	return &rel, nil
}

// archiveName is the exact name goreleaser gives a platform's archive — see
// archives.name_template in .goreleaser.yaml: cloud_<version>_<os>_<arch>,
// .zip on Windows and .tar.gz everywhere else. version has no leading "v".
func archiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("cloud_%s_%s_%s%s", version, goos, goarch, ext)
}

// pickAsset finds a platform's archive and the checksums file, by exact name.
// A substring match would also find the archive's SBOM
// (cloud_…_linux_amd64.tar.gz.sbom.json), so nothing but the exact name will do.
func pickAsset(rel *release, goos, goarch string) (asset, sums releaseAsset) {
	want := archiveName(strings.TrimPrefix(rel.TagName, "v"), goos, goarch)
	for _, a := range rel.Assets {
		switch a.Name {
		case "checksums.txt":
			sums = a
		case want:
			asset = a
		}
	}
	return asset, sums
}

// checksumFor reads the expected SHA-256 of one asset from checksums.txt.
func checksumFor(ctx context.Context, rawURL, assetName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("could not download checksums.txt: %w", hideURL(err))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading checksums.txt: HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == assetName {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt does not list %s", assetName)
}

// downloadCap bounds a download whose size the release does not list.
// Generous: a release archive is a few tens of MiB.
const downloadCap = 200 << 20

// download streams a URL into a temporary file and returns its path.
//
// size is what the release metadata says the asset weighs. Nothing larger is
// accepted — not by Content-Length, not by the bytes that actually arrive —
// so a hostile or broken server cannot fill the disk. When the size is not
// listed (zero), downloadCap applies instead.
func download(ctx context.Context, rawURL string, size int64) (string, error) {
	limit := size
	if limit <= 0 || limit > downloadCap {
		limit = downloadCap
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("could not download the release: %w", hideURL(err))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading the release: HTTP %d", res.StatusCode)
	}
	if res.ContentLength > limit {
		return "", fmt.Errorf("the download is %d bytes, more than the %d the release lists — nothing was changed", res.ContentLength, limit)
	}
	f, err := os.CreateTemp("", "cloud-update-*")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("the download is more than the %d bytes the release lists — nothing was changed", limit)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func fileSHA256(path string) (string, error) {
	// #nosec G304 -- path is the archive this process just downloaded.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// replaceBinary moves the new binary over the old one.
//
// A rename is atomic within a filesystem and works even while the old binary
// is running, because the running process holds its inode. A cross-device
// rename cannot happen here: the new file was extracted into the target's own
// directory precisely so that it cannot.
func replaceBinary(newPath, target string) error {
	return replaceBinaryOn(runtime.GOOS, os.Rename, newPath, target)
}

// staleSuffix names where Windows' running binary is moved aside to.
const staleSuffix = ".old"

// replaceBinaryOn is replaceBinary with the platform and the rename made
// explicit, so the Windows path can be exercised on any OS.
//
// Windows will not replace an executable while it runs, but it will rename
// one. So there it is two renames — the running binary aside to <target>.old,
// then the new one into place — and if the second fails the first is undone,
// which keeps the promise that a failed update leaves the old binary working.
func replaceBinaryOn(goos string, rename func(oldpath, newpath string) error, newPath, target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if err := os.Chmod(newPath, info.Mode().Perm()); err != nil {
		return err
	}
	if goos != "windows" {
		if err := rename(newPath, target); err != nil {
			if os.IsPermission(err) {
				return fmt.Errorf("cannot write %s — rerun with sudo, or set CLOUD_INSTALL_DIR to somewhere you own and reinstall: %w", target, err)
			}
			return err
		}
		return nil
	}

	stale := target + staleSuffix
	// A leftover from an earlier update would block the rename below.
	if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
		return windowsWriteError(target, err)
	}
	if err := rename(target, stale); err != nil {
		return windowsWriteError(target, err)
	}
	if err := rename(newPath, target); err != nil {
		if rbErr := rename(stale, target); rbErr != nil {
			return fmt.Errorf("could not install the new binary (%w), nor put the old one back (%w) — it is at %s; rename it to %s", err, rbErr, stale, target)
		}
		return windowsWriteError(target, err)
	}
	return nil
}

// windowsWriteError explains a failed write on Windows, where sudo is not the
// answer: the usual causes are a directory the user cannot write, or another
// copy of cloud.exe holding the file open.
func windowsWriteError(target string, err error) error {
	if os.IsPermission(err) {
		return fmt.Errorf("cannot replace %s — close any other running cloud.exe and try again; if it is installed somewhere you cannot write (such as Program Files), reinstall per-user with install.ps1, which needs no administrator: %w", target, err)
	}
	return fmt.Errorf("cannot replace %s: %w", target, err)
}

// removeStaleBinary deletes the <target>.old a Windows update leaves behind.
// It cannot be deleted during that update, since it is still the running
// image; by the next run it is not. Failure is harmless and ignored — the
// next update tries again.
func removeStaleBinary(target string) {
	_ = os.Remove(target + staleSuffix)
}

// packageManagerOf names the manager that owns a path, or "" when nothing does.
func packageManagerOf(path string) string {
	switch {
	case strings.Contains(path, "/Cellar/"), strings.Contains(path, "/homebrew/"), strings.Contains(path, "/linuxbrew/"):
		return "Homebrew"
	case strings.HasPrefix(path, "/snap/"):
		return "snap"
	case strings.HasPrefix(path, "/nix/store/"):
		return "Nix"
	}
	return ""
}

func upgradeCommandFor(manager string) string {
	switch manager {
	case "Homebrew":
		return "brew upgrade cloud"
	case "snap":
		return "snap refresh cloud"
	case "Nix":
		return "nix profile upgrade"
	}
	return "your package manager's upgrade command"
}

// confirmUpdate asks before replacing the binary, unless stdin is not a
// terminal — in which case it refuses rather than proceeding unattended.
func confirmUpdate(cmd *cobra.Command, latest string) error {
	in := cmd.InOrStdin()
	f, isFile := in.(*os.File)
	if !isFile {
		return &UsageError{errors.New("refusing to replace the binary without confirmation; pass --yes in scripts")}
	}
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return &UsageError{errors.New("refusing to replace the binary without confirmation; pass --yes in scripts")}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Replace this binary with cloud %s? Type yes to confirm: ", latest)
	var answer string
	_, _ = fmt.Fscanln(in, &answer)
	if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
		return &UsageError{errors.New("nothing was changed")}
	}
	return nil
}

func init() {
	updateCmd.Flags().BoolVar(&updateCheckOnly, "check", false, "report what is available and change nothing")
	updateCmd.Flags().BoolVarP(&updateYes, "yes", "y", false, "replace the binary without confirming")
	rootCmd.AddCommand(updateCmd)
}

// updateSizeLimit caps an extracted binary, as a guard against a malformed or
// hostile archive filling the disk. Generous: the CLI is a few tens of MiB.
const updateSizeLimit = 512 << 20

// extractBinary pulls the `cloud` binary out of a release archive and writes
// it into dir, which is the target binary's own directory so the later rename
// cannot cross a filesystem.
//
// Only the one entry named "cloud" (or "cloud.exe") is extracted, by exact
// name: an archive is untrusted input, and walking it into arbitrary paths is
// how tar extraction becomes a path-traversal bug.
func extractBinary(archive, dir string) (string, error) {
	want := "cloud"
	if runtime.GOOS == "windows" {
		want = "cloud.exe"
	}
	out, err := os.CreateTemp(dir, ".cloud-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	// Covers the error paths; on success copyOut has already closed it, and
	// the second Close's error means nothing.
	defer func() { _ = out.Close() }()

	// copyOut writes the binary and makes sure it reached the disk: a Sync or
	// Close error means a short or unwritten file, which must not be renamed
	// over a working binary.
	copyOut := func(r io.Reader) error {
		n, err := io.Copy(out, io.LimitReader(r, updateSizeLimit))
		if err != nil {
			return err
		}
		if n == updateSizeLimit {
			return errors.New("the binary in the release archive is implausibly large")
		}
		if err := out.Sync(); err != nil {
			return fmt.Errorf("writing the new binary: %w", err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("writing the new binary: %w", err)
		}
		return nil
	}

	if runtime.GOOS == "windows" {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			_ = os.Remove(out.Name())
			return "", err
		}
		defer func() { _ = zr.Close() }()
		for _, f := range zr.File {
			if path.Base(f.Name) != want {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				_ = os.Remove(out.Name())
				return "", err
			}
			err = copyOut(rc)
			_ = rc.Close()
			if err != nil {
				_ = os.Remove(out.Name())
				return "", err
			}
			return out.Name(), nil
		}
		_ = os.Remove(out.Name())
		return "", fmt.Errorf("the release archive contains no %s", want)
	}

	// #nosec G304 -- archive is the file this process just downloaded.
	f, err := os.Open(archive)
	if err != nil {
		_ = os.Remove(out.Name())
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = os.Remove(out.Name())
		return "", fmt.Errorf("the release archive is not a gzip file: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = os.Remove(out.Name())
			return "", err
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != want {
			continue
		}
		if err := copyOut(tr); err != nil {
			_ = os.Remove(out.Name())
			return "", err
		}
		return out.Name(), nil
	}
	_ = os.Remove(out.Name())
	return "", fmt.Errorf("the release archive contains no %s", want)
}

// hideURL drops the request URL net/http puts into a transport error. That
// URL is where the release files are hosted, which is not an address users
// should be sent to; the cause (refused, timed out, DNS) is what they need.
func hideURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
