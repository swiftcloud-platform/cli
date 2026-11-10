package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/auth"
	"cloud/internal/config"
	"cloud/internal/version"
)

var logoutAll bool

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Sign out: end the session and forget the stored credential",
	Long: `Sign out of the API URL in use: ask the platform to end the session,
then delete the stored credential. Credentials are kept per API host, so this
signs you out of that one host and leaves any others alone; "cloud whoami"
shows which host you are about to affect. --all deletes every stored
credential, for every host.

Only a session from "cloud login" can be ended from here. An API token pasted
with --token-stdin is forgotten locally but stays valid until it expires or is
revoked in the dashboard (Settings → API tokens).

CLOUD_TOKEN is not stored, so logout cannot remove it: unset it as well.

Logout works even when the API URL is one the CLI refuses to talk to — a
retired hostname, say — since forgetting a credential needs only the host. No
request is sent to such a URL.`,
	Example: `  cloud logout
  cloud logout --all                                          # every host
  CLOUD_API_URL=http://localhost:5173/api/v1 cloud logout    # just the dev host`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		w := cmd.ErrOrStderr()
		store, err := credentialStore()
		if err != nil {
			return err
		}
		if strings.TrimSpace(os.Getenv("CLOUD_TOKEN")) != "" {
			fmt.Fprintln(w, "warning: CLOUD_TOKEN is set and still authenticates every command; logout cannot remove it — unset it too.")
		}

		// End the session on the server first, while the token is still at
		// hand. Best effort: a failure is reported, and the local copy is
		// deleted regardless.
		if config.ValidateAPIURL(cfg.APIURL) == nil {
			if cred, err := store.Stored(cfg.APIURL); err == nil {
				revokeSession(cmd.Context(), w, cred)
			}
		}

		if logoutAll {
			removed, err := store.ClearAll()
			if err != nil {
				return err
			}
			if len(removed) == 0 {
				fmt.Fprintln(w, "No stored credentials.")
				return nil
			}
			fmt.Fprintf(w, "Deleted stored credentials for %s.\n", strings.Join(removed, ", "))
			if len(removed) > 1 {
				fmt.Fprintf(w, "Only the session for %s was ended on the server; sessions for the other hosts stay valid there until they expire.\n", apiHost(cfg.APIURL))
			}
			return nil
		}
		if err := store.Clear(cfg.APIURL); err != nil {
			return err
		}
		fmt.Fprintf(w, "Signed out of %s.\n", cfg.APIURL)
		return nil
	},
}

// revokeSession asks the platform to end a `cloud login` session. An API
// token cannot be revoked with itself here; the message says where instead.
func revokeSession(ctx context.Context, w io.Writer, cred *auth.Credential) {
	if cred.Kind != auth.KindSession {
		fmt.Fprintln(w, "The stored credential is an API token: it is forgotten here but stays valid until it expires or is revoked in the dashboard.")
		return
	}
	authBase, err := auth.BaseFromAPI(cfg.APIURL)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	flow := &auth.DeviceFlow{AuthBase: authBase, UserAgent: version.UserAgent()}
	if err := flow.SignOut(ctx, cred.Token); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("timed out")
		}
		fmt.Fprintf(w, "warning: could not end the session on the server (%v); it stays valid there until it expires.\n", err)
	}
}

func init() {
	logoutCmd.Flags().BoolVar(&logoutAll, "all", false, "delete the stored credentials for every API host")
	rootCmd.AddCommand(logoutCmd)
}
