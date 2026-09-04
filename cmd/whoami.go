package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
)

type whoami struct {
	Email     string   `json:"email" yaml:"email"`
	Name      string   `json:"name" yaml:"name"`
	AuthKind  string   `json:"authKind" yaml:"authKind"`
	ExpiresAt *string  `json:"expiresAt" yaml:"expiresAt"`
	APIURL    string   `json:"apiUrl" yaml:"apiUrl"`
	Context   string   `json:"context" yaml:"context"`
	Org       string   `json:"org" yaml:"org"`
	Region    string   `json:"region" yaml:"region"`
	Orgs      []orgRow `json:"organizations" yaml:"organizations"`
}

type orgRow struct {
	Slug string `json:"slug" yaml:"slug"`
	Name string `json:"name" yaml:"name"`
	Role string `json:"role" yaml:"role"`
}

type orgRows []orgRow

func (r orgRows) Columns() []string { return []string{"SLUG", "NAME", "ROLE"} }
func (r orgRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, o := range r {
		out[i] = []string{o.Slug, o.Name, o.Role}
	}
	return out
}
func (r orgRows) IDs() []string {
	out := make([]string, len(r))
	for i, o := range r {
		out[i] = o.Slug
	}
	return out
}

func orgRowsFrom(orgs []api.Org) orgRows {
	var rows orgRows
	for _, o := range orgs {
		rows = append(rows, orgRow{Slug: o.Slug, Name: o.Name, Role: string(o.Role)})
	}
	return rows
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show who you are signed in as, and where the CLI points",
	Long: `Show the signed-in account, the API URL in use, the default
organisation and region, and where each of those values came from — flag,
environment, context or built-in default.

Run this first whenever a command says you are not signed in: credentials are
stored per API host, so a session for one API URL does not apply to another.`,
	Example: `  cloud whoami
  cloud whoami -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, cred, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetMeWithResponse(cmd.Context())
		if err != nil {
			return fmt.Errorf("could not reach %s: %w", cfg.APIURL, err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		me, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		w := whoami{
			Email: me.User.Email, Name: deref(me.User.Name),
			AuthKind: string(cred.Kind), APIURL: cfg.APIURL, Context: cfg.ContextName, Org: cfg.Org, Region: cfg.Region,
			Orgs: orgRowsFrom(me.Organizations),
		}
		w.AuthKind = string(me.Auth.Kind)
		var exp time.Time
		if me.Auth.ExpiresAt != nil {
			exp = *me.Auth.ExpiresAt
		} else if !cred.ExpiresAt.IsZero() {
			exp = cred.ExpiresAt
		}
		if !exp.IsZero() {
			s := exp.UTC().Format(time.RFC3339)
			w.ExpiresAt = &s
		}

		if printer.Format != output.Table {
			return printer.Print(w)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Signed in as   %s", w.Email)
		if w.Name != "" {
			fmt.Fprintf(out, " (%s)", w.Name)
		}
		fmt.Fprintf(out, "\nCredential     %s", w.AuthKind)
		if !exp.IsZero() {
			left := time.Until(exp)
			fmt.Fprintf(out, ", expires %s", exp.Local().Format("Mon 2 Jan 15:04"))
			if left < 24*time.Hour {
				fmt.Fprintf(out, "  ⚠ in %s", left.Round(time.Minute))
			}
		}
		fmt.Fprintf(out, "\nAPI            %s", w.APIURL)
		if w.Context != "" {
			fmt.Fprintf(out, "  (context %s)", w.Context)
		}
		fmt.Fprintf(out, "\nOrganisation   %s\nRegion         %s\n\n", orDash(w.Org), orDash(w.Region))
		return printer.Print(orgRows(w.Orgs))
	},
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func init() { rootCmd.AddCommand(whoamiCmd) }
