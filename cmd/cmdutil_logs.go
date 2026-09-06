package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

/*
Log streaming, shared by `app logs` and `db logs`.

The one behaviour worth writing down: a log stream that yields nothing is
reported, not passed off as success. An empty stdout is indistinguishable from
"the app is quiet", and someone debugging a deployment cannot tell whether
their process wrote nothing, the platform collected nothing, or the command
went to the wrong place. Saying so — on stderr, so a pipe still sees only log
lines — costs nothing and answers the question.
*/

// streamLines copies a log stream to stdout line by line and reports how many
// lines it wrote. A cancelled context (Ctrl-C while following) is not an error.
func streamLines(cmd *cobra.Command, body io.Reader) (int, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	out := cmd.OutOrStdout()
	lines := 0
	for sc.Scan() {
		fmt.Fprintln(out, sc.Text())
		lines++
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) && cmd.Context().Err() == nil {
		return lines, err
	}
	return lines, nil
}

// reportNoLogs explains an empty stream. The scale-to-zero case is named
// because it is the common one: the platform stops the pod when the app is
// idle, and a stopped pod has no logs to read — it wakes it and waits a few
// seconds, but a cold start can outlast that.
func reportNoLogs(cmd *cobra.Command, kind, name string, following bool) {
	if flagQuiet {
		return
	}
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "No log lines returned for %s %q.\n", kind, name)
	if following {
		fmt.Fprintln(w, "The stream ended without any output. If the app is idle it may have no running instance; send it a request and try again.")
		return
	}
	if kind == "app" {
		fmt.Fprintln(w, "An idle app is scaled to zero and has no instance to read from. The platform wakes it and waits a few seconds, which a slow cold start can outlast.")
		fmt.Fprintf(w, "Send it a request, then retry — or follow the stream while you do: cloud app logs %s -f\n", name)
	}
	fmt.Fprintf(w, "Check it is running with `cloud %s get %s`.\n", map[string]string{"app": "app", "database": "db"}[kind], name)
}
