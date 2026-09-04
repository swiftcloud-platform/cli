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
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/version"
)

/*
Self-update.

The rules this follows, and why:

  - Nothing is replaced until the download's SHA-256 matches the release's own
    checksums.txt. An interrupted or tampered download must not become the
    binary a person then trusts with their infrastructure.
  - The new binary is written beside the old one and renamed into place, so the
    swap is atomic on the same filesystem and a failure leaves the working
    binary exactly where it was.
  - A CLI installed by a package manager is not updated in place. Homebrew
    tracks its own files, and overwriting them silently would break `brew
    upgrade` later; the command says which upgrade to run instead.
  - "dev" builds are refused, because there is no version to compare and
    overwriting someone's locally built binary is never what they meant.
*/

const (
	// updateRepo is where releases are published.
	updateRepo = "swiftcloud-platform/cli"
	// updateAPI is GitHub's release metadata. Overridden in tests.
	updateAPI = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
)

var (
	updateCheckOnly bool
	updateYes       bool
	// updateAPIURL is the metadata endpoint, injectable for tests.
	updateAPIURL = updateAPI
)

// release is the part of GitHub's release JSON this needs.
type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update this CLI to the latest release",
	Long: `Download the latest release and replace this binary with it.

The download's checksum is verified against the release's own checksums.txt
before anything is replaced, and the new binary is renamed into place, so a
failed update leaves the working one untouched.

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

		rel, err := latestRelease(cmd.Context())
		if err != nil {
			return err
		}
		latest := strings.TrimPrefix(rel.TagName, "v")
		if latest == "" {
			return fmt.Errorf("the latest release from %s has no version tag", updateRepo)
		}

		if printer.Format != "table" {
			return printer.Print(map[string]string{
				"current":   current,
				"latest":    latest,
				"url":       rel.HTMLURL,
				"installed": self,
			})
		}

		if current == latest {
			fmt.Fprintf(errOut, "cloud %s is the latest release.\n", current)
			return nil
		}
		fmt.Fprintf(errOut, "cloud %s is available (you have %s)\n%s\n", latest, current, rel.HTMLURL)
		if updateCheckOnly {
			return nil
		}

		// A package manager's files are not ours to replace.
		if manager := packageManagerOf(self); manager != "" {
			return &UsageError{fmt.Errorf("this CLI was installed with %s, which manages its own files — run `%s` instead",
				manager, upgradeCommandFor(manager))}
		}
		if current == "dev" {
			return &UsageError{errors.New("this is a local build (version \"dev\"), so there is nothing to compare — install a release, or build again from source")}
		}

		asset, sums := pickAsset(rel)
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
		if err := installUpdate(cmd.Context(), asset.URL, asset.Name, sums.URL, self); err != nil {
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
func installUpdate(ctx context.Context, assetURL, assetName, sumsURL, target string) error {
	archive, err := download(ctx, assetURL)
	if err != nil {
		return err
	}
	defer os.Remove(archive)

	want, err := checksumFor(ctx, sumsURL, assetName)
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
	defer os.Remove(binary)

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
		return nil, fmt.Errorf("could not reach GitHub to look for a release: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s has published no releases yet", updateRepo)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered HTTP %d looking for the latest release", res.StatusCode)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("could not read GitHub's release metadata: %w", err)
	}
	return &rel, nil
}

// pickAsset finds this platform's archive and the checksums file.
func pickAsset(rel *release) (asset, sums struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}) {
	wantOS, wantArch := runtime.GOOS, runtime.GOARCH
	for _, a := range rel.Assets {
		name := strings.ToLower(a.Name)
		switch {
		case name == "checksums.txt":
			sums = a
		case strings.Contains(name, wantOS) && strings.Contains(name, wantArch):
			asset = a
		}
	}
	return asset, sums
}

// checksumFor reads the expected SHA-256 of one asset from checksums.txt.
func checksumFor(ctx context.Context, url, assetName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("could not download checksums.txt: %w", err)
	}
	defer res.Body.Close()
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

// download streams a URL into a temporary file and returns its path.
func download(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", url, res.StatusCode)
	}
	f, err := os.CreateTemp("", "cloud-update-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, res.Body)
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
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
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
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if err := os.Chmod(newPath, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(newPath, target); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot write %s — rerun with sudo, or set CLOUD_INSTALL_DIR to somewhere you own and reinstall: %w", target, err)
		}
		return err
	}
	return nil
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
	defer out.Close()

	copyOut := func(r io.Reader) error {
		n, err := io.Copy(out, io.LimitReader(r, updateSizeLimit))
		if err != nil {
			return err
		}
		if n == updateSizeLimit {
			return errors.New("the binary in the release archive is implausibly large")
		}
		return nil
	}

	if runtime.GOOS == "windows" {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			_ = os.Remove(out.Name())
			return "", err
		}
		defer zr.Close()
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
			rc.Close()
			if err != nil {
				_ = os.Remove(out.Name())
				return "", err
			}
			return out.Name(), nil
		}
		_ = os.Remove(out.Name())
		return "", fmt.Errorf("the release archive contains no %s", want)
	}

	f, err := os.Open(archive)
	if err != nil {
		_ = os.Remove(out.Name())
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = os.Remove(out.Name())
		return "", fmt.Errorf("the release archive is not a gzip file: %w", err)
	}
	defer gz.Close()
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
