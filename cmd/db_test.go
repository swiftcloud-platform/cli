package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
Database helpers. The credential rendering is tested hardest because it is the
one place a password is formatted: a password with a quote in it must not break
the shell line it lands in, and one with an @ must not break the URL.

Terminal statuses are not tested here — `cloud db` shares terminalStatus with
the app commands, and terminal_test.go covers it.
*/

func TestParseRetention(t *testing.T) {
	for _, ok := range []string{"3d", "7d", "30d", "2w", "1m", " 7d "} {
		got, err := parseRetention(ok)
		if err != nil {
			t.Errorf("parseRetention(%q): %v", ok, err)
			continue
		}
		if got != strings.TrimSpace(ok) {
			t.Errorf("parseRetention(%q) = %q, want it passed through untouched", ok, got)
		}
	}
	for _, bad := range []string{"", "7", "d", "0d", "-3d", "7 d", "7days", "1y", "3D"} {
		if _, err := parseRetention(bad); err == nil {
			t.Errorf("parseRetention(%q) was accepted; a window we cannot promise must be refused", bad)
		}
	}
}

func TestCredentialURL_EncodesThePassword(t *testing.T) {
	c := dbCredential{
		Engine: "postgresql", Host: "db.example.com", Port: 5432,
		Username: "app", Password: "p@ss/w:rd?", Database: "app",
	}
	got := c.connectionURL()
	if strings.Contains(got, "p@ss/w:rd?") {
		t.Fatalf("password went in raw and breaks the URL: %s", got)
	}
	if !strings.HasPrefix(got, "postgresql://app:") || !strings.HasSuffix(got, "@db.example.com:5432/app") {
		t.Errorf("unexpected URL shape: %s", got)
	}
}

func TestCredentialURL_UsesThePlatformsOwnStringWhenGiven(t *testing.T) {
	c := dbCredential{
		Engine: "postgresql", Host: "ignored", Port: 1,
		URI: "postgresql://real:string@host:5432/db?sslmode=require",
	}
	if got := c.connectionURL(); got != c.URI {
		t.Errorf("built a URL instead of using the platform's: %s", got)
	}
}

func TestCredentialURL_MariaDBScheme(t *testing.T) {
	c := dbCredential{Engine: "mariadb", Host: "h", Port: 3306, Username: "u", Password: "p", Database: "d"}
	if got := c.connectionURL(); !strings.HasPrefix(got, "mysql://") {
		t.Errorf("mariadb URL should use the mysql scheme: %s", got)
	}
}

func TestCredentialRender_EnvIsSourceable(t *testing.T) {
	c := dbCredential{
		Engine: "postgresql", Host: "h", Port: 5432,
		Username: "app", Password: "it's a $ecret", Database: "app",
	}
	out, err := c.render("env")
	if err != nil {
		t.Fatal(err)
	}
	// A quote in the password must be escaped, or the next line of the shell
	// becomes part of the string.
	if !strings.Contains(out, `PGPASSWORD='it'\''s a $ecret'`) {
		t.Errorf("password is not safely quoted:\n%s", out)
	}
	for _, want := range []string{"PGHOST='h'", "PGPORT='5432'", "PGUSER='app'", "PGDATABASE='app'", "DATABASE_URL='postgresql://"} {
		if !strings.Contains(out, want) {
			t.Errorf("env output missing %s:\n%s", want, out)
		}
	}
}

func TestCredentialRender_EnvNamesTheEnginesOwnVariables(t *testing.T) {
	pg, err := dbCredential{Engine: "postgresql", Host: "h", Username: "u", Password: "p", Database: "d"}.render("env")
	if err != nil {
		t.Fatal(err)
	}
	my, err := dbCredential{Engine: "mariadb", Host: "h", Username: "u", Password: "p", Database: "d"}.render("env")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pg, "PGUSER=") || strings.Contains(pg, "MYSQL_USER=") {
		t.Errorf("postgresql should emit PG* only:\n%s", pg)
	}
	if !strings.Contains(my, "MYSQL_PWD=") || strings.Contains(my, "PGPASSWORD=") {
		t.Errorf("mariadb should emit MYSQL_* only:\n%s", my)
	}
}

func TestCredentialRender_UnknownFormatIsAUsageError(t *testing.T) {
	_, err := dbCredential{Engine: "postgresql"}.render("yaml")
	if err == nil {
		t.Fatal("an unknown --format must be refused")
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit code = %d, want %d for a usage mistake", ExitCode(err), ExitUsage)
	}
}

// A rendered credential must never carry a trailing newline into a URL, which
// is the shape that breaks `psql "$(cloud db credentials … --format url)"`.
func TestCredentialRender_URLHasNoTrailingNewline(t *testing.T) {
	out, err := dbCredential{Engine: "postgresql", Host: "h", Port: 5432, Username: "u", Password: "p", Database: "d"}.render("url")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out, "\n\r") {
		t.Errorf("url output contains a newline: %q", out)
	}
}

/*
Database commands against a fake platform. This file carries its own fake
rather than extending the one in commands_test.go, so the two can be edited
independently.
*/

const dbJSON = `{"id":"db1","name":"orders","organizationId":"org1","region":"zm-lusaka-central-1","regionId":"r1","engine":"postgresql","version":"16","size":"db-1","description":"","status":"ready","errorMessage":"","host":"orders.db.cloud.co.zm","port":5432,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`

// fakeDBPlatform serves just enough of /api/v1 for the db commands. Handlers
// are overridable per test through the returned map.
func fakeDBPlatform(t *testing.T, override map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	json200 := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
	}
	routes := map[string]http.HandlerFunc{
		"/api/v1/me":                                        json200(`{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`),
		"/api/v1/database-engines":                          json200(`{"items":[{"engine":"postgresql","version":"16","default":true},{"engine":"mariadb","version":"11.4","default":true}]}`),
		"/api/v1/orgs/acme/databases":                       json200(`{"items":[` + dbJSON + `]}`),
		"/api/v1/orgs/acme/databases/orders":                json200(dbJSON),
		"/api/v1/orgs/acme/databases/orders/credentials":    json200(`{"username":"app","password":"p@ss'w/rd","host":"orders.db.cloud.co.zm","port":5432,"database":"app","connectionString":"host=orders.db.cloud.co.zm port=5432 user=app","connectionStringUri":"postgresql://app:p%40ss%27w%2Frd@orders.db.cloud.co.zm:5432/app?sslmode=require","internalHost":"orders.org1.svc","internalConnectionString":"postgresql://app@orders.org1.svc:5432/app"}`),
		"/api/v1/orgs/acme/databases/orders/backups":        json200(`{"items":[{"id":"b1","name":"orders-20260904","status":"completed","size":"41 MB","completedAt":"2026-09-04T02:00:00Z","createdAt":"2026-09-04T01:58:00Z"}]}`),
		"/api/v1/orgs/acme/databases/orders/backups/enable": json200(`{"enabled":true,"retention":"7d"}`),
	}
	for path, h := range override {
		routes[path] = h
	}
	for path, h := range routes {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dbSetup(t *testing.T, override map[string]http.HandlerFunc) {
	t.Helper()
	srv := fakeDBPlatform(t, override)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_REGION", "zm-lusaka-central-1")
	t.Setenv("CLOUD_TOKEN", "owner-token")
}

func TestDBList_Table(t *testing.T) {
	dbSetup(t, nil)
	out, err := run(t, "db", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "ENGINE", "orders", "postgresql", "ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestDBEngines_ShowsTheDefault(t *testing.T) {
	dbSetup(t, nil)
	out, err := run(t, "db", "engines")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "postgresql") || !strings.Contains(out, "yes") {
		t.Errorf("engines table should mark the default:\n%s", out)
	}
}

func TestDBCreate_RejectsAnUnknownEngine(t *testing.T) {
	dbSetup(t, nil)
	_, err := run(t, "db", "create", "orders", "--engine", "mysql")
	if err == nil {
		t.Fatal("an engine the platform does not offer must be refused before the call")
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit = %d, want %d", ExitCode(err), ExitUsage)
	}
	if !strings.Contains(err.Error(), "cloud db engines") {
		t.Errorf("the message should point at `cloud db engines`: %v", err)
	}
}

func TestDBCreate_NeedsARegion(t *testing.T) {
	dbSetup(t, nil)
	t.Setenv("CLOUD_REGION", "")
	_, err := run(t, "db", "create", "orders", "--engine", "postgresql")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a create with no region should be a usage error, got %v", err)
	}
}

func TestDBCredentials_URLFormatIsOneLine(t *testing.T) {
	dbSetup(t, nil)
	out, err := run(t, "db", "credentials", "orders", "--format", "url")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("url output should be a single line:\n%q", out)
	}
	// The platform's own URI is used verbatim, sslmode and all.
	if !strings.Contains(out, "sslmode=require") {
		t.Errorf("url output should be the platform's URI:\n%s", out)
	}
}

func TestDBCredentials_EnvFormatIsSafeToEval(t *testing.T) {
	dbSetup(t, nil)
	out, err := run(t, "db", "credentials", "orders", "--format", "env")
	if err != nil {
		t.Fatal(err)
	}
	// The fake's password contains a quote and a slash; both must survive.
	if !strings.Contains(out, `PGPASSWORD='p@ss'\''w/rd'`) {
		t.Errorf("password not safely quoted:\n%s", out)
	}
	if !strings.Contains(out, "PGHOST='orders.db.cloud.co.zm'") {
		t.Errorf("env output missing PGHOST:\n%s", out)
	}
}

func TestDBCredentials_JSONKeepsTheInternalHost(t *testing.T) {
	dbSetup(t, nil)
	out, err := run(t, "db", "credentials", "orders", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "internalConnectionString") {
		t.Errorf("json output should carry every field:\n%s", out)
	}
}

// Credentials before the cluster exists are a conflict, not an empty result.
func TestDBCredentials_ConflictWhileProvisioning_Exit2(t *testing.T) {
	dbSetup(t, map[string]http.HandlerFunc{
		"/api/v1/orgs/acme/databases/orders/credentials": func(w http.ResponseWriter, _ *http.Request) {
			problem(w, 409, "conflict", "The database is still provisioning; credentials are not available yet.")
		},
	})
	_, err := run(t, "db", "credentials", "orders")
	if err == nil {
		t.Fatal("a 409 must not be reported as success")
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit = %d, want %d", ExitCode(err), ExitUsage)
	}
	if !strings.Contains(err.Error(), "still provisioning") {
		t.Errorf("the platform's own sentence should survive: %v", err)
	}
}

// Backups on MariaDB are refused by the platform; the sentence must reach the
// user and the exit code must say "your request", not "we broke".
func TestDBBackupEnable_UnsupportedEngine_Exit2(t *testing.T) {
	dbSetup(t, map[string]http.HandlerFunc{
		"/api/v1/orgs/acme/databases/orders/backups/enable": func(w http.ResponseWriter, _ *http.Request) {
			problem(w, 400, "unsupported-engine", "Automatic backups are available for PostgreSQL databases only.")
		},
	})
	_, err := run(t, "db", "backup", "enable", "orders")
	if err == nil {
		t.Fatal("an unsupported engine must be an error")
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("exit = %d, want %d for unsupported-engine", ExitCode(err), ExitUsage)
	}
	if !strings.Contains(err.Error(), "PostgreSQL databases only") {
		t.Errorf("the platform's sentence should be printed verbatim: %v", err)
	}
}

func TestDBBackupEnable_BadRetentionNeverReachesThePlatform(t *testing.T) {
	dbSetup(t, map[string]http.HandlerFunc{
		"/api/v1/orgs/acme/databases/orders/backups/enable": func(w http.ResponseWriter, _ *http.Request) {
			t.Error("the platform was called with a retention the CLI should have refused")
		},
	})
	_, err := run(t, "db", "backup", "enable", "orders", "--retention", "7x")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a malformed retention should be a usage error, got %v", err)
	}
}

func TestDBBackupList_Table(t *testing.T) {
	dbSetup(t, nil)
	out, err := run(t, "db", "backup", "list", "orders")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "COMPLETED", "orders-20260904", "completed", "41 MB"} {
		if !strings.Contains(out, want) {
			t.Errorf("backup table missing %q:\n%s", want, out)
		}
	}
}

func TestDBRestore_RefusesRestoringOntoItself(t *testing.T) {
	dbSetup(t, nil)
	_, err := run(t, "db", "restore", "orders", "--to", "orders")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("restoring onto the source should be refused, got %v", err)
	}
}

func TestDBRestore_RejectsANonRFC3339Time(t *testing.T) {
	dbSetup(t, nil)
	_, err := run(t, "db", "restore", "orders", "--to", "orders-copy", "--at", "yesterday")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a bad --at should be a usage error, got %v", err)
	}
}

func TestDBDelete_RefusesWithoutConfirmationWhenNotATerminal(t *testing.T) {
	dbSetup(t, nil)
	_, err := run(t, "db", "delete", "orders")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a non-interactive delete without --yes must be refused, got %v", err)
	}
}
