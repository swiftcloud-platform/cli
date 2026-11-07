package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/termsafe"
)

// sinceSeconds turns a --since duration into the query parameter the platform
// takes, refusing what it would silently ignore. The platform ignores a
// non-positive value and caps at 30 days, so a "--since -5m" that quietly
// returned everything, or a "--since 90d" that quietly returned 30, would each
// be a small lie about what was asked for.
func sinceSeconds(d time.Duration) (*int, error) {
	if d == 0 {
		return nil, nil
	}
	if d < 0 {
		return nil, &UsageError{errors.New("--since must be positive, for example 10m")}
	}
	const maxWindow = 30 * 24 * time.Hour
	if d > maxWindow {
		return nil, &UsageError{fmt.Errorf("--since is capped at 30 days by the platform; %s is longer", d)}
	}
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return &secs, nil
}

/*
Log streaming for `app logs`.

The one behaviour worth writing down: a log stream that yields nothing is
reported, not passed off as success. An empty stdout is indistinguishable from
"the app is quiet", and someone debugging a deployment cannot tell whether
their process wrote nothing, the platform collected nothing, or the command
went to the wrong place. Saying so — on stderr, so a pipe still sees only log
lines — costs nothing and answers the question.

`app logs -f` is not cut off by the API client's request timeout: the client
sends GET …/logs?follow=true through a path with no overall deadline (see
api.New), bounded instead by the time to response headers and by Ctrl-C.
*/

// maxLogLine is the longest log line printed whole. A longer one is cut here
// and marked, rather than ending the stream: one runaway line (a minified
// payload, a binary dump) must not take the rest of the logs with it.
const maxLogLine = 64 << 10

// truncatedMark ends a log line that was cut at maxLogLine.
const truncatedMark = " …[truncated]"

// streamLines copies a log stream to stdout line by line and reports how many
// lines it wrote. A cancelled context (Ctrl-C while following) is not an error.
//
// When stdout is a terminal each line is stripped of control characters: log
// lines are written by whatever runs in the app, and an escape sequence in one
// would be interpreted by the person's terminal. Into a pipe or file they pass
// through untouched, since a program reading them may want the bytes.
func streamLines(cmd *cobra.Command, body io.Reader) (int, error) {
	return copyLines(cmd, body, cmd.OutOrStdout(), isTerminal(cmd.OutOrStdout()))
}

func copyLines(cmd *cobra.Command, body io.Reader, out io.Writer, sanitise bool) (int, error) {
	r := bufio.NewReaderSize(body, 64<<10)
	lines := 0
	line := make([]byte, 0, 4096)
	truncated, pending := false, false
	emit := func() {
		text := string(line)
		if sanitise {
			text = termsafe.String(text)
		}
		if truncated {
			text += truncatedMark
		}
		fmt.Fprintln(out, text)
		lines++
		line, truncated, pending = line[:0], false, false
	}
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err == nil {
			pending = true
			if room := maxLogLine - len(line); len(chunk) > room {
				line = append(line, chunk[:room]...)
				truncated = true
			} else {
				line = append(line, chunk...)
			}
			if !isPrefix {
				emit()
			}
			continue
		}
		// A last line without a newline still counts.
		if pending {
			emit()
		}
		if errors.Is(err, io.EOF) || cmd.Context().Err() != nil {
			return lines, nil
		}
		return lines, err
	}
}

// isTerminal reports whether w is a character device, i.e. a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// reportNoLogs explains an empty stream. The scale-to-zero case is named
// because it is the common one: the platform stops the pod when the app is
// idle, and a stopped pod has no logs to read — it wakes it and waits a few
// seconds, but a cold start can outlast that.
func reportNoLogs(cmd *cobra.Command, name string, following bool) {
	if flagQuiet {
		return
	}
	w := cmd.ErrOrStderr()
	name = termsafe.String(name)
	fmt.Fprintf(w, "No log lines returned for app %q.\n", name)
	if following {
		fmt.Fprintln(w, "The stream ended without any output. If the app is idle it may have no running instance; send it a request and try again.")
		return
	}
	fmt.Fprintln(w, "An idle app is scaled to zero and has no instance to read from. The platform wakes it and waits a few seconds, which a slow cold start can outlast.")
	fmt.Fprintf(w, "Send it a request, then retry — or follow the stream while you do: cloud app logs %s -f\n", name)
	fmt.Fprintf(w, "Check it is running with `cloud app get %s`.\n", name)
}
