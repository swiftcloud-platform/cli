// Package output renders command results in the format the user asked for.
//
// Every command that prints a resource goes through here, so `--output json`
// is uniform and scriptable everywhere, and `--quiet` prints only the one
// identifier a script would capture.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// Format is the requested rendering.
type Format string

// The output formats every command accepts through --output.
const (
	Table Format = "table"
	JSON  Format = "json"
	YAML  Format = "yaml"
)

// ParseFormat validates a --output value.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(s) {
	case "", "table":
		return Table, nil
	case "json":
		return JSON, nil
	case "yaml", "yml":
		return YAML, nil
	}
	return "", fmt.Errorf("unknown output format %q (want table, json or yaml)", s)
}

// Printer writes results.
type Printer struct {
	W      io.Writer
	Format Format
	Quiet  bool
}

// Tabular is anything printable as a table: Columns gives the header order,
// Rows one line each, and IDs the identifiers --quiet prints.
type Tabular interface {
	Columns() []string
	Rows() [][]string
	// IDs returns the identifiers printed in --quiet mode, one per row.
	IDs() []string
}

// Print renders v. For table output v must implement Tabular; for json/yaml
// any value is serialised as-is so scripts see the full resource.
func (p *Printer) Print(v any) error {
	if p.Quiet {
		if t, ok := v.(Tabular); ok {
			for _, id := range t.IDs() {
				fmt.Fprintln(p.W, id)
			}
			return nil
		}
	}
	switch p.Format {
	case JSON:
		enc := json.NewEncoder(p.W)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case YAML:
		return yaml.NewEncoder(p.W).Encode(v)
	default:
		t, ok := v.(Tabular)
		if !ok {
			return fmt.Errorf("value of type %T cannot be rendered as a table; use --output json", v)
		}
		tw := tabwriter.NewWriter(p.W, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, strings.Join(t.Columns(), "\t"))
		for _, r := range t.Rows() {
			fmt.Fprintln(tw, strings.Join(r, "\t"))
		}
		return tw.Flush()
	}
}
