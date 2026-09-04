package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/auth"
	"cloud/internal/config"
	"cloud/internal/version"
	"cloud/internal/wait"
)

/*
Shared plumbing for commands: building an authenticated client from the
resolved configuration and stored credential, turning platform errors into
sentences and exit codes, confirming destructive actions, and waiting on a
resource until its status is terminal.
*/

// AuthError marks "not signed in / token rejected" so main can exit 3.
type AuthError struct{ Err error }

func (e *AuthError) Error() string { return e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

// credentialStore is where the current context's credential lives.
func credentialStore() (*auth.Store, error) {
	dir, err := config.Dir(os.Getenv)
	if err != nil {
		return nil, err
	}
	return &auth.Store{Dir: dir, Env: os.Getenv}, nil
}

// apiClient builds a client for the resolved API URL with the stored credential.
// Commands that need no credential (login, version) do not call this.
func apiClient() (*api.ClientWithResponses, *auth.Credential, error) {
	store, err := credentialStore()
	if err != nil {
		return nil, nil, err
	}
	cred, err := store.Load(cfg.APIURL)
	if err != nil {
		return nil, nil, &AuthError{err}
	}
	if cred.Expired(time.Now()) {
		return nil, nil, &AuthError{fmt.Errorf("your sign-in expired on %s — run `cloud login` again", cred.ExpiresAt.Local().Format("2 Jan 15:04"))}
	}
	c, err := api.New(api.Options{BaseURL: cfg.APIURL, Token: cred.Token, UserAgent: version.UserAgent()})
	if err != nil {
		return nil, nil, err
	}
	return c, cred, nil
}

// check wraps api.Check so an unauthenticated response becomes an AuthError.
func check(res interface {
	StatusCode() int
}, raw *api.ClientWithResponses, body []byte, status int) error {
	_ = raw
	_ = res
	if status >= 200 && status < 300 {
		return nil
	}
	e := api.ErrorFromResponse(status, body)
	if errors.Is(e, api.ErrUnauthenticated) {
		return &AuthError{fmt.Errorf("%s (run `cloud login`, or check CLOUD_TOKEN)", e.Detail)}
	}
	return e
}

// apiErr is the short form used by every command: status + body → error.
func apiErr(status int, body []byte) error {
	return check(nil, nil, body, status)
}

// requireOrg returns the organisation to act in, from --org / CLOUD_ORG /
// context, with a hint when none is set.
func requireOrg() (string, error) {
	if cfg.Org == "" {
		return "", &UsageError{errors.New("no organisation selected — pass --org <slug>, set CLOUD_ORG, or run `cloud org use <slug>`")}
	}
	return cfg.Org, nil
}

// confirm asks the user to type the resource name back, unless --yes was given.
// Soft-deleted names are reusable on this platform, so the name is the whole
// safeguard against deleting the wrong thing from muscle memory.
func confirm(cmd *cobra.Command, yes bool, what, name string) error {
	if yes {
		return nil
	}
	in := cmd.InOrStdin()
	f, isFile := in.(*os.File)
	interactive := false
	if isFile {
		if st, err := f.Stat(); err == nil && (st.Mode()&os.ModeCharDevice) != 0 {
			interactive = true
		}
	}
	if !interactive {
		return &UsageError{fmt.Errorf("refusing to delete %s %q without confirmation; pass --yes in scripts", what, name)}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "This will permanently delete %s %q. Type the name to confirm: ", what, name)
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != name {
		return &UsageError{errors.New("names did not match; nothing was deleted")}
	}
	return nil
}

// healthProbe adapts /health to wait.Health.
func healthProbe(c *api.ClientWithResponses) wait.Health {
	return func(ctx context.Context) (bool, time.Duration, error) {
		res, err := c.GetHealthWithResponse(ctx)
		if err != nil {
			return true, 0, err // cannot tell; do not blame the worker
		}
		var h *api.Health
		if res.JSON200 != nil {
			h = res.JSON200
		} else if res.JSON503 != nil {
			h = res.JSON503
		}
		if h == nil {
			return true, 0, errors.New("health response not understood")
		}
		ok := string(h.Worker.Status) == "ok"
		var age time.Duration
		if h.Worker.AgeSeconds != nil {
			age = time.Duration(*h.Worker.AgeSeconds) * time.Second
		}
		return ok, age, nil
	}
}

// appTerminal says whether an app status is one worth stopping at.
func appTerminal(status string) (done bool, failed bool) {
	switch status {
	case "running", "stopped", "suspended":
		return true, false
	case "failed", "error", "delete_failed":
		return true, true
	}
	return false, false
}

// waitForApp polls GET app until terminal, printing status changes to stderr.
func waitForApp(cmd *cobra.Command, c *api.ClientWithResponses, org, name string, timeout time.Duration) (*api.App, error) {
	var last *api.App
	poll := func(ctx context.Context) (string, bool, error) {
		res, err := c.GetOrgsOrgAppsAppWithResponse(ctx, org, name)
		if err != nil {
			return "", false, err
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return "", false, err
		}
		if res.JSON200 == nil {
			return "", false, fmt.Errorf("unexpected response from %s", cfg.APIURL)
		}
		last = res.JSON200
		done, failed := appTerminal(last.Status)
		if failed {
			return last.Status, false, &wait.ErrFailed{Status: last.Status}
		}
		return last.Status, done, nil
	}
	_, err := wait.Until(cmd.Context(), poll, healthProbe(c), wait.Options{
		Interval: 3 * time.Second,
		Timeout:  timeout,
		OnStatus: func(s string) {
			if !flagQuiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", s)
			}
		},
	})
	return last, err
}

// deref returns "" for a nil string pointer (generated optional fields).
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// stdinToken reads a token from stdin for --token-stdin.
func stdinToken(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", &UsageError{errors.New("no token on stdin")}
	}
	if strings.ContainsAny(tok, " \t\n") {
		return "", &UsageError{errors.New("stdin must contain exactly one token")}
	}
	return tok, nil
}

// decoded returns the 2xx body, or an error when the platform answered
// 2xx with something the client could not decode (a proxy page, say).
func decoded[T any](p *T) (*T, error) {
	if p == nil {
		return nil, fmt.Errorf("unexpected response from %s: success status without a JSON body", cfg.APIURL)
	}
	return p, nil
}
