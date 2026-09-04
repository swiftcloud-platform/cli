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
			return nil
		}

		authBase, err := auth.AuthBaseFromAPI(cfg.APIURL)
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
		if len(me.Organizations) > 0 && cfg.Org == "" {
			orgs := me.Organizations
			if len(orgs) == 1 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Organisation: %s. Set it as default with `cloud org use %s`.\n", orgs[0].Slug, orgs[0].Slug)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "You belong to %d organisations; pick one with `cloud org use <slug>`.\n", len(orgs))
			}
		}
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
