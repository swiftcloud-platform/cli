// Package version holds the build metadata injected by the release pipeline.
package version

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Set records the values injected at link time. Called once from main.
//
// A leading "v" is dropped, so a build from `git describe` (v1.2.3) reports
// itself the same way as one from goreleaser (1.2.3).
func Set(v, c, d string) {
	version, commit, date = strings.TrimPrefix(v, "v"), c, d
}

// Version is the semantic version (or "dev" for a local build).
func Version() string { return version }

// Long is the full, human-readable build description.
func Long() string {
	return fmt.Sprintf("cloud %s (commit %s, built %s)", version, commit, date)
}

// UserAgent identifies the CLI to the API and to the S3 endpoint.
func UserAgent() string { return "swiftcloud-cli/" + version }

// describeSuffix is what `git describe` appends to a tag when the build is not
// exactly at that tag: "-<commits since>-g<abbreviated hash>".
var describeSuffix = regexp.MustCompile(`-[0-9]+-g[0-9a-f]{4,}$`)

// Release reports whether v names a published release, and returns it in the
// canonical "vMAJOR.MINOR.PATCH[-prerelease]" form that semver.Compare takes.
//
// Only a clean release version qualifies: "dev", an empty string, a bare
// commit hash, a build from a dirty tree ("-dirty"), a build some commits past
// a tag (git describe's "-N-g<hash>"), and anything carrying build metadata
// ("+…") are all local builds, which have no meaningful place in the release
// order and must never be overwritten by — or compared against — a release.
func Release(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(v, "+") || strings.HasSuffix(v, "-dirty") || describeSuffix.MatchString(v) {
		return "", false
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return "", false
	}
	// semver accepts the shorthand "v1" and "v1.2"; a release tag is always
	// the full three numbers.
	if semver.Canonical(v) != v {
		return "", false
	}
	return v, true
}
