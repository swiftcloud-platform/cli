package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud/internal/api"
)

/*
DNS records. The rule worth guarding is that a record's identity is its id,
not its name — a name can carry several records — and that the CLI refuses
what the name servers would refuse rather than spending a round trip on it.
*/

type dnsFake struct {
	records []api.DnsRecord
	posted  map[string]any
	putBody map[string]any
	deleted string
	// method records which spelling the CLI actually used, and rejectPatch
	// simulates a platform that predates the PATCH route.
	method      string
	rejectPatch bool
}

func dnsSetup(t *testing.T, records []api.DnsRecord) *dnsFake {
	t.Helper()
	f := &dnsFake{records: records}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/domains", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"d1","name":"example.com","status":"active","nameservers":["ns1.swiftcloud.africa","ns2.swiftcloud.africa"],"dnsSynced":true,"createdAt":"2026-09-01T00:00:00Z"}]}`)
	})
	mux.HandleFunc("/api/v1/orgs/acme/domains/example.com/records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&f.posted)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"r9","name":"www","type":"A","content":"203.0.113.10","ttl":300,"priority":0,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`)
			return
		}
		b, _ := json.Marshal(map[string]any{"items": f.records})
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/v1/orgs/acme/domains/example.com/records/r1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			if f.rejectPatch {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			f.method = r.Method
			_ = json.NewDecoder(r.Body).Decode(&f.putBody)
			fmt.Fprint(w, `{"id":"r1","name":"www","type":"A","content":"203.0.113.20","ttl":300,"priority":0,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`)
		case http.MethodPut:
			f.method = r.Method
			_ = json.NewDecoder(r.Body).Decode(&f.putBody)
			fmt.Fprint(w, `{"id":"r1","name":"www","type":"A","content":"203.0.113.20","ttl":300,"priority":0,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`)
		case http.MethodDelete:
			f.deleted = "r1"
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_TOKEN", "owner-token")
	return f
}

func TestDnsList_SaysWhetherDNSIsDelegated(t *testing.T) {
	dnsSetup(t, nil)
	out, err := run(t, "dns", "list")
	if err != nil {
		t.Fatal(err)
	}
	// "not delegated" is the thing a user needs to know: records created here
	// will not answer until the registrar points at these name servers.
	if !strings.Contains(out, "delegated here") || !strings.Contains(out, "ns1.swiftcloud.africa") {
		t.Errorf("expected delegation state and name servers:\n%s", out)
	}
}

func TestDnsRecordsList_ShowsIDsAndHidesIrrelevantPriority(t *testing.T) {
	dnsSetup(t, []api.DnsRecord{
		{Id: "r1", Name: "www", Type: "A", Content: "203.0.113.10", Ttl: 300, Priority: 0},
		{Id: "r2", Name: "@", Type: "MX", Content: "mail.example.com.", Ttl: 3600, Priority: 10},
	})
	out, err := run(t, "dns", "records", "list", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "r1") || !strings.Contains(out, "r2") {
		t.Errorf("ids are how records are addressed and must be shown:\n%s", out)
	}
	if !strings.Contains(out, "10") {
		t.Errorf("an MX priority should be shown:\n%s", out)
	}
}

// Checked on the rows rather than the rendered text: a null priority decodes
// as 0, and 0 is a real MX priority, so the column must be empty for types
// where priority means nothing — and scraping the text would confuse it with
// a TTL that happens to end in a zero.
func TestDnsRecordRows_PriorityOnlyForMXAndSRV(t *testing.T) {
	rows := dnsRecordRows{
		{Id: "r1", Name: "www", Type: "A", Content: "203.0.113.10", Ttl: 300, Priority: 0},
		{Id: "r2", Name: "@", Type: "MX", Content: "mail.example.com.", Ttl: 3600, Priority: 10},
		{Id: "r3", Name: "@", Type: "MX", Content: "backup.example.com.", Ttl: 3600, Priority: 0},
		{Id: "r4", Name: "_sip._tcp", Type: "SRV", Content: "10 5060 sip.example.com.", Ttl: 300, Priority: 5},
	}.Rows()
	if rows[0][5] != "" {
		t.Errorf("an A record must show no priority, got %q", rows[0][5])
	}
	if rows[1][5] != "10" {
		t.Errorf("an MX priority must be shown, got %q", rows[1][5])
	}
	// Priority 0 on an MX is meaningful — highest preference — and must not
	// be hidden just because it is the zero value.
	if rows[2][5] != "0" {
		t.Errorf("MX priority 0 is real and must be shown, got %q", rows[2][5])
	}
	if rows[3][5] != "5" {
		t.Errorf("an SRV priority must be shown, got %q", rows[3][5])
	}
}

// MX and SRV need a priority and the platform would refuse without one, so
// the CLI says so without spending a round trip.
func TestDnsRecordsAdd_RequiresPriorityForMXAndSRV(t *testing.T) {
	f := dnsSetup(t, nil)
	for _, typ := range []string{"MX", "SRV"} {
		_, err := run(t, "dns", "records", "add", "example.com", "--name", "@", "--type", typ, "--content", "mail.example.com")
		if err == nil || ExitCode(err) != ExitUsage {
			t.Errorf("%s without --priority should be a usage error, got %v", typ, err)
		}
	}
	if f.posted != nil {
		t.Errorf("nothing should have been sent: %v", f.posted)
	}
}

func TestDnsRecordsAdd_RejectsAnUnknownType(t *testing.T) {
	dnsSetup(t, nil)
	_, err := run(t, "dns", "records", "add", "example.com", "--name", "www", "--type", "AAA", "--content", "x")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("an unknown type should be refused locally, got %v", err)
	}
	if !strings.Contains(err.Error(), "AAAA") {
		t.Errorf("the message should list the valid types: %v", err)
	}
}

func TestDnsRecordsAdd_SendsWhatWasAsked(t *testing.T) {
	f := dnsSetup(t, nil)
	if _, err := run(t, "dns", "records", "add", "example.com",
		"--name", "www", "--type", "a", "--content", "203.0.113.10", "--ttl", "600"); err != nil {
		t.Fatal(err)
	}
	if f.posted["name"] != "www" || f.posted["content"] != "203.0.113.10" {
		t.Errorf("body = %v", f.posted)
	}
	// Lowercase input is normalised, since DNS types are conventionally upper.
	if f.posted["type"] != "A" {
		t.Errorf("type = %v, want A", f.posted["type"])
	}
	if f.posted["ttl"] != float64(600) {
		t.Errorf("ttl = %v", f.posted["ttl"])
	}
}

// An omitted field must not be sent at all, or an update would overwrite it.
func TestDnsRecordsUpdate_SendsOnlyWhatChanged(t *testing.T) {
	f := dnsSetup(t, nil)
	if _, err := run(t, "dns", "records", "update", "example.com", "r1", "--content", "203.0.113.20"); err != nil {
		t.Fatal(err)
	}
	if f.putBody["content"] != "203.0.113.20" {
		t.Errorf("content not sent: %v", f.putBody)
	}
	for _, absent := range []string{"name", "type", "ttl", "priority"} {
		if _, present := f.putBody[absent]; present {
			t.Errorf("%s was sent but not asked for: %v", absent, f.putBody)
		}
	}
}

func TestDnsRecordsUpdate_NothingToChange(t *testing.T) {
	dnsSetup(t, nil)
	_, err := run(t, "dns", "records", "update", "example.com", "r1")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("an update with no flags should be a usage error, got %v", err)
	}
}

func TestDnsRecordsRemove_NeedsConfirmation(t *testing.T) {
	f := dnsSetup(t, nil)
	_, err := run(t, "dns", "records", "remove", "example.com", "r1")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("removing without --yes should be refused, got %v", err)
	}
	if f.deleted != "" {
		t.Errorf("nothing should have been deleted")
	}
}

func TestDnsRecordsRemove_WithYes(t *testing.T) {
	f := dnsSetup(t, nil)
	if _, err := run(t, "dns", "records", "remove", "example.com", "r1", "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.deleted != "r1" {
		t.Errorf("the record was not deleted")
	}
}

// The platform is moving record updates from PUT to PATCH. Both spellings
// exist across the transition, so the CLI must work against a platform on
// either side of it — a released binary that only spoke one would break the
// day the other landed.
func TestDnsRecordsUpdate_PrefersPatch(t *testing.T) {
	f := dnsSetup(t, nil)
	if _, err := run(t, "dns", "records", "update", "example.com", "r1", "--ttl", "600"); err != nil {
		t.Fatal(err)
	}
	if f.method != http.MethodPatch {
		t.Errorf("used %s, want PATCH when the platform offers it", f.method)
	}
}

func TestDnsRecordsUpdate_FallsBackToPutOn405(t *testing.T) {
	f := dnsSetup(t, nil)
	f.rejectPatch = true // a platform that predates the PATCH route
	if _, err := run(t, "dns", "records", "update", "example.com", "r1", "--ttl", "600"); err != nil {
		t.Fatal(err)
	}
	if f.method != http.MethodPut {
		t.Errorf("used %s, want a PUT fallback when PATCH is not allowed", f.method)
	}
	if f.putBody["ttl"] != float64(600) {
		t.Errorf("the body must survive the fallback: %v", f.putBody)
	}
}
