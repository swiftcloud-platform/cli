// Package spice provides an embedded SPICE client for VM console access.
//
// The package embeds the spice-client JavaScript library and serves it
// through a local HTTP server, allowing the CLI to open a SPICE console
// in the user's browser without any system-level SPICE dependencies.
package spice

import _ "embed"

// spice-client.min.js is the minified spice-client bundle (IIFE format)
// from https://www.npmjs.com/package/spice-client.
//
//go:embed spice-client.min.js
var jsBundle []byte

//go:embed index.html.tmpl
var indexHTML string
