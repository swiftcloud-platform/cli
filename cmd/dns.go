package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
)

/*
Domains and their DNS records.

Three things here are not obvious from the API.

The platform's name servers are the authority, not the database. A record is
written to them first and recorded second, so a refusal is a real refusal and
the CLI passes the sentence through rather than reporting success. This exists
because the reverse used to be true: records were written to the database
only, and the hourly mirror deleted them again — a record you added would
simply vanish.

Content is canonicalised by the platform: hostnames come back with a trailing
dot, TXT comes back quoted. That is what the name servers hold, so it is what
`records list` shows, even though it is not quite what you typed.

A record's identity is its id, not its name: a name can carry several records
(two A records, several TXT), so update and remove take the id from
`records list`.
*/

var (
	dnsName     string
	dnsType     string
	dnsContent  string
	dnsTTL      int
	dnsPriority int
	dnsYes      bool
)

// recordTypes is the set the platform accepts, kept here so a typo is refused
// before a round trip and the message can list the alternatives.
var recordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA", "PTR"}

func validRecordType(t string) (string, error) {
	up := strings.ToUpper(strings.TrimSpace(t))
	for _, known := range recordTypes {
		if up == known {
			return up, nil
		}
	}
	return "", &UsageError{fmt.Errorf("--type %q is not one of %s", t, strings.Join(recordTypes, ", "))}
}

// ── table shapes ────────────────────────────────────────────────────────────

type domainRowsList []api.Domain

func (r domainRowsList) Columns() []string { return []string{"DOMAIN", "STATUS", "DNS", "NAMESERVERS"} }
func (r domainRowsList) Rows() [][]string {
	out := make([][]string, len(r))
	for i, d := range r {
		dns := "not delegated"
		if d.DnsSynced {
			dns = "delegated here"
		}
		out[i] = []string{d.Name, d.Status, dns, strings.Join(d.Nameservers, " ")}
	}
	return out
}
func (r domainRowsList) IDs() []string {
	out := make([]string, len(r))
	for i, d := range r {
		out[i] = d.Name
	}
	return out
}

type dnsRecordRows []api.DnsRecord

func (r dnsRecordRows) Columns() []string {
	return []string{"ID", "NAME", "TYPE", "CONTENT", "TTL", "PRIORITY"}
}
func (r dnsRecordRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, rec := range r {
		// A null priority decodes as 0, and 0 is a legitimate MX priority, so
		// the value alone cannot say whether one was set. The type can:
		// priority means nothing for anything but MX and SRV, so it is shown
		// for those and blank otherwise.
		priority := ""
		if t := string(rec.Type); t == "MX" || t == "SRV" {
			priority = strconv.Itoa(rec.Priority)
		}
		out[i] = []string{rec.Id, rec.Name, string(rec.Type), rec.Content, strconv.Itoa(rec.Ttl), priority}
	}
	return out
}
func (r dnsRecordRows) IDs() []string {
	out := make([]string, len(r))
	for i, rec := range r {
		out[i] = rec.Id
	}
	return out
}

// ── commands ────────────────────────────────────────────────────────────────

var domainsCmd = &cobra.Command{
	Use:   "domains",
	Short: "Domains the organisation holds",
	Long: `The domains this organisation holds, and whether the platform's name
servers are authoritative for each.

A domain is added in the dashboard; the CLI lists them and manages their
records with "cloud dns".`,
	Example: `  cloud domains list
  cloud dns records list example.com`,
}

var domainsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the organisation's domains",
	Long: `List domains, with whether DNS is delegated to the platform.

"not delegated" means the domain's registrar still points elsewhere, so
records created here will not answer until the name servers shown are set at
the registrar.`,
	Example: `  cloud domains list
  cloud domains list -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgDomainsWithResponse(cmd.Context(), org)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if len(list.Items) == 0 && !flagQuiet && printer.Format == output.Table {
			fmt.Fprintln(cmd.ErrOrStderr(), "No domains in this organisation. Add one in the dashboard.")
			return nil
		}
		return printer.Print(domainRowsList(list.Items))
	},
}

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "DNS records on the organisation's domains",
	Long: `Read and change the DNS records the platform's name servers hold.

Records are written to the name servers first and recorded second, so a
refusal from them is reported rather than swallowed — if a record appears
here, it is live.

A name can carry several records, so a record is identified by its id from
"records list" rather than by name.`,
	Example: `  cloud dns records list example.com
  cloud dns records add example.com --name www --type A --content 203.0.113.10
  cloud dns records remove example.com <id>`,
}

var dnsRecordsCmd = &cobra.Command{
	Use:   "records",
	Short: "List and change DNS records",
	Example: `  cloud dns records list example.com
  cloud dns records add example.com --name @ --type MX --content mail.example.com --priority 10`,
}

var dnsRecordsListCmd = &cobra.Command{
	Use:   "list <domain>",
	Short: "List a domain's DNS records",
	Long: `List the records the name servers hold for a domain.

Content is shown as the name servers hold it — hostnames end with a dot and
TXT values are quoted — which is not always exactly what was typed to create
them.`,
	Example: `  cloud dns records list example.com
  cloud dns records list example.com -o json
  cloud dns records list example.com --quiet    # ids only, for scripts`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgDomainsDomainRecordsWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if len(list.Items) == 0 && !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "No records on %s.\n", args[0])
			return nil
		}
		return printer.Print(dnsRecordRows(list.Items))
	},
}

var dnsRecordsAddCmd = &cobra.Command{
	Use:   "add <domain> --name <n> --type <t> --content <v>",
	Short: "Create a DNS record",
	Long: `Create a record on a domain.

--name is relative to the domain: "@" is the domain itself, "www" is
www.<domain>. A full name inside the domain works too and is normalised.

--content depends on the type:
  A      an IPv4 address            AAAA   an IPv6 address
  CNAME  a hostname                 NS     a hostname
  MX     a hostname, with --priority
  SRV    "<weight> <port> <target>", with --priority
  TXT    plain text; the platform adds the quoting
  CAA    "<flags> <tag> <value>"    PTR    a hostname

--ttl defaults to 300 seconds for address-like records and 3600 for the rest.

The record is written to the name servers before it is recorded, so if this
command succeeds the record is live, and if the name servers refuse it you
get their reason rather than a success you cannot rely on.`,
	Example: `  cloud dns records add example.com --name www --type A --content 203.0.113.10
  cloud dns records add example.com --name @ --type MX --content mail.example.com --priority 10
  cloud dns records add example.com --name @ --type TXT --content "v=spf1 include:_spf.example.com ~all"
  cloud dns records add example.com --name _sip._tcp --type SRV --content "10 5060 sip.example.com" --priority 5`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if dnsName == "" || dnsType == "" || dnsContent == "" {
			return &UsageError{fmt.Errorf("--name, --type and --content are all required")}
		}
		t, err := validRecordType(dnsType)
		if err != nil {
			return err
		}
		// The platform requires it and would refuse the write; saying so here
		// costs no round trip and names the flag.
		if (t == "MX" || t == "SRV") && !cmd.Flags().Changed("priority") {
			return &UsageError{fmt.Errorf("--priority is required for %s records", t)}
		}
		body := api.PostOrgsOrgDomainsDomainRecordsJSONRequestBody{
			Name: dnsName, Type: api.DnsRecordCreateType(t), Content: dnsContent,
		}
		if cmd.Flags().Changed("ttl") {
			body.Ttl = &dnsTTL
		}
		if cmd.Flags().Changed("priority") {
			body.Priority = &dnsPriority
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgDomainsDomainRecordsWithResponse(cmd.Context(), org, args[0], body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		rec, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Created %s %s %s (id %s, ttl %d) — live on the name servers.\n",
				rec.Name, rec.Type, rec.Content, rec.Id, rec.Ttl)
			return nil
		}
		return printer.Print(dnsRecordRows{*rec})
	},
}

var dnsRecordsUpdateCmd = &cobra.Command{
	Use:   "update <domain> <record-id>",
	Short: "Change a DNS record",
	Long: `Change one or more fields of an existing record. Fields you do not
pass are left alone.

The id comes from "cloud dns records list", because a name can carry several
records and the name alone would be ambiguous.`,
	Example: `  cloud dns records update example.com <id> --content 203.0.113.20
  cloud dns records update example.com <id> --ttl 3600
  cloud dns records update example.com <id> --content mail2.example.com --priority 20`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		body := api.PutOrgsOrgDomainsDomainRecordsRecordIdJSONRequestBody{}
		changed := false
		if cmd.Flags().Changed("name") {
			body.Name = &dnsName
			changed = true
		}
		if cmd.Flags().Changed("type") {
			t, err := validRecordType(dnsType)
			if err != nil {
				return err
			}
			rt := api.DnsRecordUpdateType(t)
			body.Type = &rt
			changed = true
		}
		if cmd.Flags().Changed("content") {
			body.Content = &dnsContent
			changed = true
		}
		if cmd.Flags().Changed("ttl") {
			body.Ttl = &dnsTTL
			changed = true
		}
		if cmd.Flags().Changed("priority") {
			body.Priority = &dnsPriority
			changed = true
		}
		if !changed {
			return &UsageError{fmt.Errorf("nothing to change — pass --name, --type, --content, --ttl or --priority")}
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PutOrgsOrgDomainsDomainRecordsRecordIdWithResponse(cmd.Context(), org, args[0], args[1], body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		rec, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Updated %s %s %s (ttl %d) — live on the name servers.\n",
				rec.Name, rec.Type, rec.Content, rec.Ttl)
			return nil
		}
		return printer.Print(dnsRecordRows{*rec})
	},
}

var dnsRecordsRemoveCmd = &cobra.Command{
	Use:   "remove <domain> <record-id>",
	Short: "Delete a DNS record",
	Long: `Delete a record from the name servers.

Anything relying on it stops resolving as caches expire, which can take up to
the record's TTL, so you are asked to confirm the id unless --yes.`,
	Example: `  cloud dns records list example.com
  cloud dns records remove example.com <id>
  cloud dns records remove example.com <id> --yes`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if err := confirm(cmd, dnsYes, "record", args[1]); err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.DeleteOrgsOrgDomainsDomainRecordsRecordIdWithResponse(cmd.Context(), org, args[0], args[1])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Deleted %s from %s. Resolvers may keep it until its TTL expires.\n", args[1], args[0])
		}
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{dnsRecordsAddCmd, dnsRecordsUpdateCmd} {
		c.Flags().StringVar(&dnsName, "name", "", `record name relative to the domain: "@", "www", …`)
		c.Flags().StringVar(&dnsType, "type", "", "A, AAAA, CNAME, MX, TXT, NS, SRV, CAA or PTR")
		c.Flags().StringVar(&dnsContent, "content", "", "the record's value; see the help for each type")
		c.Flags().IntVar(&dnsTTL, "ttl", 0, "seconds, 60–86400 (default 300, or 3600 for MX/TXT/NS/CAA)")
		c.Flags().IntVar(&dnsPriority, "priority", 0, "MX and SRV only")
	}
	dnsRecordsRemoveCmd.Flags().BoolVarP(&dnsYes, "yes", "y", false, "skip the confirmation (scripts)")

	dnsRecordsCmd.AddCommand(dnsRecordsListCmd, dnsRecordsAddCmd, dnsRecordsUpdateCmd, dnsRecordsRemoveCmd)
	dnsCmd.AddCommand(dnsRecordsCmd)
	domainsCmd.AddCommand(domainsListCmd)
	rootCmd.AddCommand(dnsCmd, domainsCmd)
}
