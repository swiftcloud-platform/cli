// cloud is the SwiftCloud command-line interface.
//
// Version information is injected at build time by goreleaser (or the
// Makefile) through -ldflags "-X main.version=… -X main.commit=… -X main.date=…".
// The build-info package is imported under an alias so that `version` can be
// the variable those flags target; a trailing underscore would work too, but
// it is not a Go name and the flags would read oddly.
package main

import (
	"fmt"
	"os"

	"cloud/cmd"
	buildinfo "cloud/internal/version"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	buildinfo.Set(version, commit, date)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(cmd.ExitCode(err))
	}
}
