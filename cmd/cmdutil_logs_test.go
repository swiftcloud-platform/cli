package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func logCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	return c
}

// One runaway line used to end the whole stream with "token too long".
func TestCopyLines_TruncatesALongLineAndCarriesOn(t *testing.T) {
	long := strings.Repeat("x", 3*maxLogLine)
	in := "first\n" + long + "\nlast"
	var out bytes.Buffer
	n, err := copyLines(logCmd(), strings.NewReader(in), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("lines = %d, want 3", n)
	}
	got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if got[0] != "first" || got[2] != "last" {
		t.Errorf("neighbours of the long line damaged: %q / %q", got[0], got[2])
	}
	if len(got[1]) != maxLogLine+len(truncatedMark) || !strings.HasSuffix(got[1], truncatedMark) {
		t.Errorf("long line not cut at %d and marked: len %d", maxLogLine, len(got[1]))
	}
}

func TestCopyLines_KeepsBlankLinesAndCRLF(t *testing.T) {
	var out bytes.Buffer
	n, _ := copyLines(logCmd(), strings.NewReader("a\r\n\nb\n"), &out, false)
	if n != 3 || out.String() != "a\n\nb\n" {
		t.Errorf("%d %q", n, out.String())
	}
}

// On a terminal, an app's log line must not be able to drive it.
func TestCopyLines_SanitisesForATerminalOnly(t *testing.T) {
	in := "ok \x1b[2J\x1b]0;pwned\x07 done\ttab\n"
	var tty, pipe bytes.Buffer
	_, _ = copyLines(logCmd(), strings.NewReader(in), &tty, true)
	_, _ = copyLines(logCmd(), strings.NewReader(in), &pipe, false)
	if strings.ContainsAny(tty.String(), "\x1b\x07") || !strings.Contains(tty.String(), "\ttab") {
		t.Errorf("terminal output = %q", tty.String())
	}
	if pipe.String() != in {
		t.Errorf("a pipe must get the bytes as sent: %q", pipe.String())
	}
}
