package cmd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/auth"
	"cloud/internal/config"
	"cloud/internal/termsafe"
	"cloud/internal/version"
)

var (
	loginTokenStdin bool
	loginNoBrowser  bool
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to SwiftCloud",
	Long: `Sign in with a short code you approve in your browser.

The terminal shows a code and a URL. Open the URL on any device where you are
signed in to SwiftCloud, check the code matches, and approve. No password ever
passes through the terminal, and it works over SSH.

For scripts and CI, pipe an API token from the dashboard instead:
  echo "$TOKEN" | cloud login --token-stdin
or skip login entirely and set CLOUD_TOKEN.

Signing in picks an organisation for you — the first you belong to — and saves
it, so the CLI is usable immediately. An --org flag, CLOUD_ORG, or a context
that already names one all take precedence; change it later with
"cloud org use <slug>".

The credential is stored per API host, so signing in to a staging or local API
does not disturb your production session — and vice versa.`,
	Example: `  cloud login
  cloud login --no-browser                       print the URL, do not open it

  # a token from the dashboard, for CI
  echo "$CLOUD_API_TOKEN" | cloud login --token-stdin

  # sign in to a local or staging API instead of production
  CLOUD_API_URL=http://localhost:5173/api/v1 cloud login`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		store, err := credentialStore()
		if err != nil {
			return err
		}
		if loginTokenStdin {
			tok, err := stdinToken(cmd.InOrStdin())
			if err != nil {
				return err
			}
			cred := &auth.Credential{Token: tok, Kind: auth.KindOfToken(tok)}
			// Prove it before saving: a bad paste should fail here, not later.
			me, err := whoAmI(cmd.Context(), cfg.APIURL, tok)
			if err != nil {
				return err
			}
			if me.Auth.ExpiresAt != nil {
				cred.ExpiresAt = *me.Auth.ExpiresAt
			}
			if err := store.Save(cfg.APIURL, cred); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Signed in as %s (%s token) on %s\n", termsafe.String(me.User.Email), cred.Kind, cfg.APIURL)
			adoptDefaultOrg(cmd, me)
			return nil
		}

		authBase, err := auth.BaseFromAPI(cfg.APIURL)
		if err != nil {
			return &UsageError{err}
		}
		w := cmd.ErrOrStderr()
		// Say where before anything else: the code about to be shown grants
		// a session on this host, and an --api-url or CLOUD_API_URL someone
		// else chose would otherwise be invisible.
		host, defHost := apiHost(cfg.APIURL), apiHost(config.DefaultAPIURL)
		fmt.Fprintf(w, "Signing in to %s\n", host)
		if host != defHost {
			fmt.Fprintf(w, "warning: this is not the default API (%s); the API URL comes from %s. Approve only if you meant to sign in here.\n",
				defHost, orDash(cfg.Source["api_url"]))
		}

		flow := &auth.DeviceFlow{AuthBase: authBase, UserAgent: version.UserAgent()}
		dc, err := flow.Start(cmd.Context())
		if err != nil {
			return err
		}
		// The approval URL is opened in a browser and shown as the place to
		// approve; it must be the platform's own page, not wherever the
		// response said.
		approveURL, err := auth.VerificationURL(dc, cfg.APIURL)
		if err != nil {
			return err
		}

		code := termsafe.String(dc.UserCode)
		if len(code) == 8 {
			code = code[:4] + "-" + code[4:]
		}
		fmt.Fprintf(w, "\nYour code is:  %s\n\nOpen %s\nand approve this device. Waiting… (Ctrl-C to cancel)\n\n", code, approveURL)
		if !loginNoBrowser {
			if err := openBrowser(approveURL); err != nil {
				fmt.Fprintf(w, "Could not open a browser (%v); open the URL above yourself.\n", err)
			}
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), dc.Expiry())
		defer cancel()
		tok, err := flow.Poll(ctx, dc)
		if errors.Is(err, context.DeadlineExceeded) {
			return auth.ErrExpired
		}
		if err != nil {
			return err
		}
		cred := &auth.Credential{Token: tok.AccessToken, Kind: auth.KindSession}
		if tok.ExpiresIn > 0 {
			cred.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		}
		if err := store.Save(cfg.APIURL, cred); err != nil {
			return err
		}
		me, err := whoAmI(cmd.Context(), cfg.APIURL, tok.AccessToken)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Signed in as %s. Session valid until %s.\n", termsafe.String(me.User.Email), cred.ExpiresAt.Local().Format("Mon 2 Jan 15:04"))
		adoptDefaultOrg(cmd, me)
		return nil
	},
}

// whoAmI calls /me with an explicit token (before it is stored).
func whoAmI(ctx context.Context, apiURL, token string) (*api.Me, error) {
	c, err := api.New(api.Options{BaseURL: apiURL, Token: token, UserAgent: version.UserAgent()})
	if err != nil {
		return nil, err
	}
	res, err := c.GetMeWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", apiURL, err)
	}
	if err := apiErr(res.StatusCode(), res.Body); err != nil {
		return nil, err
	}
	me, err := decoded(res.JSON200)
	if err != nil {
		return nil, err
	}
	if me.User.Id == "" {
		return nil, fmt.Errorf("unexpected response from %s", apiURL)
	}
	return me, nil
}

// openBrowser starts the system's URL handler on url and reports whether it
// could be started. Callers always print the URL as well, so a failure here
// costs a copy and paste, not the operation. A variable so tests can stub it.
var openBrowser = func(url string) error {
	var c *exec.Cmd
	// #nosec G204 -- the URL is passed as a single argument, not through a
	// shell, so it cannot inject a command. Every caller passes a URL it has
	// validated or built itself: the device-login approval URL is checked by
	// auth.VerificationURL (https, on the API's host), and the console URL is
	// derived from the configured API URL with the VM id path-escaped.
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	if err := c.Start(); err != nil {
		return err
	}
	// Reap it in the background; its exit status is not ours to report.
	go func() { _ = c.Wait() }()
	return nil
}

func init() {
	loginCmd.Flags().BoolVar(&loginTokenStdin, "token-stdin", false, "read an API token from stdin instead of using the browser flow")
	loginCmd.Flags().BoolVar(&loginNoBrowser, "no-browser", false, "print the URL but do not try to open a browser")
	rootCmd.AddCommand(loginCmd)
}

// adoptDefaultOrg picks an organisation so that signing in leaves the CLI
// ready to use, rather than one instruction short of it.
//
// Only when nothing has chosen one already: an --org flag, CLOUD_ORG, or a
// context that names one all win, because they are deliberate and this is a
// guess. The first organisation is the guess — for the overwhelmingly common
// case of belonging to exactly one, it is not really a guess at all, and for
// the rest it is a starting point the message names and says how to change.
//
// A failure to save is reported but does not fail the login: the sign-in
// itself succeeded, and telling someone their login failed because a
// convenience could not be written would be worse than the inconvenience.
func adoptDefaultOrg(cmd *cobra.Command, me *api.Me) {
	w := cmd.ErrOrStderr()
	if len(me.Organizations) == 0 {
		fmt.Fprintln(w, "You do not belong to an organisation yet. Create one in the dashboard, then run `cloud org list`.")
		return
	}
	if cfg.Org != "" {
		// Already chosen deliberately; say which, so the choice is visible.
		fmt.Fprintf(w, "Organisation: %s (from %s).\n", cfg.Org, cfg.Source["org"])
		return
	}
	first := me.Organizations[0]
	slug := termsafe.String(first.Slug)
	name, err := saveDefaultOrg(w, first.Slug)
	if err != nil {
		fmt.Fprintf(w, "Organisation: %s. Could not save it as your default (%v) — set it with `cloud org use %s`.\n",
			slug, err, slug)
		return
	}
	if len(me.Organizations) == 1 {
		fmt.Fprintf(w, "Organisation: %s, saved as the default for context %q.\n", slug, name)
		return
	}
	fmt.Fprintf(w, "Organisation: %s, saved as the default for context %q. You belong to %d — switch with `cloud org use <slug>`, and see them with `cloud org list`.\n",
		slug, name, len(me.Organizations))
}
