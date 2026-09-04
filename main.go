// cloud is the SwiftCloud command-line interface.
//
// Version information is injected at build time by goreleaser (or the
// Makefile) through -ldflags "-X main.version=… -X main.commit=… -X main.date=…".
package main

import (
	"fmt"
	"os"

	"cloud/cmd"
	"cloud/internal/version"
)

var (
	version_ = "dev"
	commit   = "none"
	date     = "unknown"
)

func main() {
	version.Set(version_, commit, date)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(cmd.ExitCode(err))
	}
}
