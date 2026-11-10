package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type apps []struct {
	Name   string `json:"name" yaml:"name"`
	Status string `json:"status" yaml:"status"`
	URL    string `json:"url" yaml:"url"`
}

func (a apps) Columns() []string { return []string{"NAME", "STATUS", "URL"} }
func (a apps) Rows() [][]string {
	out := make([][]string, len(a))
	for i, x := range a {
		out[i] = []string{x.Name, x.Status, x.URL}
	}
	return out
}
func (a apps) IDs() []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = x.Name
	}
	return out
}

var sample = apps{
	{"web", "running", "https://web.apps.cloud.co.zm"},
	{"api", "deploying", ""},
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"": Table, "table": Table, "JSON": JSON, "yaml": YAML, "yml": YAML} {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("unknown format must be rejected, not silently defaulted")
	}
}

func TestTable_HeaderAndAlignedRows(t *testing.T) {
	var b bytes.Buffer
	if err := (&Printer{W: &b, Format: Table}).Print(sample); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines:\n%s", len(lines), b.String())
	}
	if !strings.HasPrefix(lines[0], "NAME") || !strings.Contains(lines[0], "STATUS") {
		t.Errorf("header wrong: %q", lines[0])
	}
	// Columns line up: STATUS starts at the same offset in every line.
	off := strings.Index(lines[0], "STATUS")
	if strings.Index(lines[1], "running") != off || strings.Index(lines[2], "deploying") != off {
		t.Errorf("columns not aligned:\n%s", b.String())
	}
}

func TestJSON_IsTheFullResource(t *testing.T) {
	var b bytes.Buffer
	if err := (&Printer{W: &b, Format: JSON}).Print(sample); err != nil {
		t.Fatal(err)
	}
	var back apps
	if err := json.Unmarshal(b.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if len(back) != 2 || back[0].URL != sample[0].URL {
		t.Errorf("JSON must carry every field, got %+v", back)
	}
}

func TestYAML(t *testing.T) {
	var b bytes.Buffer
	if err := (&Printer{W: &b, Format: YAML}).Print(sample); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "name: web") || !strings.Contains(b.String(), "status: deploying") {
		t.Errorf("yaml output wrong:\n%s", b.String())
	}
}

func TestQuiet_PrintsOnlyIdentifiers(t *testing.T) {
	var b bytes.Buffer
	if err := (&Printer{W: &b, Format: Table, Quiet: true}).Print(sample); err != nil {
		t.Fatal(err)
	}
	if b.String() != "web\napi\n" {
		t.Errorf("quiet must print one id per line and nothing else, got %q", b.String())
	}
}

func TestTable_RefusesNonTabularWithAHint(t *testing.T) {
	var b bytes.Buffer
	err := (&Printer{W: &b, Format: Table}).Print(map[string]int{"a": 1})
	if err == nil || !strings.Contains(err.Error(), "--output json") {
		t.Errorf("want an error pointing at --output json, got %v", err)
	}
}

// Table cells and --quiet IDs come from the platform, and another org member
// can set some of them (a VM description, a key label). An escape sequence in
// one must not reach the terminal, and a tab or newline must not break the
// columns.
func TestTableStripsControlCharacters(t *testing.T) {
	hostile := apps{{"web\x1b]52;c;ZXZpbA==\x07", "run\tning", "https://x\r\nFAKE"}}

	var buf bytes.Buffer
	if err := (&Printer{W: &buf, Format: Table}).Print(hostile); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\x07\r") {
		t.Fatalf("control characters reached the table: %q", out)
	}
	if lines := strings.Split(strings.TrimRight(out, "\n"), "\n"); len(lines) != 2 {
		t.Fatalf("a cell broke the row into %d lines: %q", len(lines), out)
	}

	buf.Reset()
	if err := (&Printer{W: &buf, Format: Table, Quiet: true}).Print(hostile); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); strings.ContainsAny(got, "\x1b\x07") || got != "web]52;c;ZXZpbA==\n" {
		t.Fatalf("quiet output = %q", got)
	}
}
