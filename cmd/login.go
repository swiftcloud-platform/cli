package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/auth"
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
			fmt.Fprintf(cmd.ErrOrStderr(), "Signed in as %s (%s token) on %s\n", me.User.Email, cred.Kind, cfg.APIURL)
			adoptDefaultOrg(cmd, me)
			return nil
		}

		authBase, err := auth.BaseFromAPI(cfg.APIURL)
		if err != nil {
			return &UsageError{err}
		}
		flow := &auth.DeviceFlow{AuthBase: authBase}
		dc, err := flow.Start(cmd.Context())
		if err != nil {
			return err
		}

		code := dc.UserCode
		if len(code) == 8 {
			code = code[:4] + "-" + code[4:]
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "\nYour code is:  %s\n\nOpen %s\nand approve this device. Waiting… (Ctrl-C to cancel)\n\n", code, dc.VerificationURIComplete)
		if !loginNoBrowser {
			openBrowser(dc.VerificationURIComplete)
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(dc.ExpiresIn)*time.Second)
		defer cancel()
		tok, err := flow.Poll(ctx, dc)
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
		fmt.Fprintf(cmd.ErrOrStderr(), "Signed in as %s. Session valid until %s.\n", me.User.Email, cred.ExpiresAt.Local().Format("Mon 2 Jan 15:04"))
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

// openBrowser is best-effort; the URL is always printed as well.
func openBrowser(url string) {
	var c *exec.Cmd
	// #nosec G204 -- the URL is an argument, not a shell string: exec.Command
	// does not interpret it, so there is nothing to inject into, and it comes
	// from the platform's own device-code response.
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
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
	name, err := saveDefaultOrg(first.Slug)
	if err != nil {
		fmt.Fprintf(w, "Organisation: %s. Could not save it as your default (%v) — set it with `cloud org use %s`.\n",
			first.Slug, err, first.Slug)
		return
	}
	if len(me.Organizations) == 1 {
		fmt.Fprintf(w, "Organisation: %s, saved as the default for context %q.\n", first.Slug, name)
		return
	}
	fmt.Fprintf(w, "Organisation: %s, saved as the default for context %q. You belong to %d — switch with `cloud org use <slug>`, and see them with `cloud org list`.\n",
		first.Slug, name, len(me.Organizations))
}
