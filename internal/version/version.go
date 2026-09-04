// Package version holds the build metadata injected by the release pipeline.
package version

import "fmt"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Set records the values injected at link time. Called once from main.
func Set(v, c, d string) {
	version, commit, date = v, c, d
}

// Version is the semantic version (or "dev" for a local build).
func Version() string { return version }

// Long is the full, human-readable build description.
func Long() string {
	return fmt.Sprintf("cloud %s (commit %s, built %s)", version, commit, date)
}

// UserAgent identifies the CLI to the API and to the S3 endpoint.
func UserAgent() string { return "swiftcloud-cli/" + version }
