package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
Command tests against a fake platform. They check what a person sees: the
table, the JSON, the one-line quiet output, the sentence on failure, and the
exit code a script would branch on. The fake speaks the real wire shapes.
*/

const appJSON = `{"id":"app1","name":"web","organizationId":"org1","region":"zm-lusaka-central-1","regionId":"r1","image":"nginx:1","description":"","status":"running","url":"https://web.apps.cloud.co.zm","containerPort":8080,"replicasMin":0,"replicasMax":3,"size":"app-1","envVars":{"A":"1"},"registryAuth":null,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`

func problem(w http.ResponseWriter, status int, typ, detail string) {
	w.Header().Set("content-type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "https://cloud.co.zm/api/v1/problems/" + typ, "title": typ, "status": status, "detail": detail})
}

func fakePlatform(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	auth := func(r *http.Request) string { return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") }
	guard := func(w http.ResponseWriter, r *http.Request) bool {
		switch auth(r) {
		case "owner-token", "viewer-token":
			return true
		}
		problem(w, 401, "unauthenticated", "Sign in with `cloud login` or send an API token.")
		return false
	}
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		role := "owner"
		if auth(r) == "viewer-token" {
			role = "viewer"
		}
		_, _ = w.Write([]byte(`{"user":{"id":"u1","email":"a@b.zm","name":"Arthur"},"auth":{"kind":"api-token","expiresAt":"2030-01-01T00:00:00Z"},"organizations":[{"id":"org1","slug":"acme","name":"Acme","role":"` + role + `"}]}`))
	})
	mux.HandleFunc("/api/v1/orgs", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"org1","slug":"acme","name":"Acme","role":"owner"},{"id":"org2","slug":"beta","name":"Beta Ltd","role":"viewer"}]}`))
	})
	mux.HandleFunc("/api/v1/regions", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"r1","name":"zm-lusaka-central-1","location":"Lusaka, Zambia","status":"active"}]}`))
	})
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","api":"v1","database":"ok","worker":{"status":"ok","lastHeartbeat":"2026-09-04T00:00:00Z","ageSeconds":2},"time":"2026-09-04T00:00:00Z"}`))
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			if auth(r) == "viewer-token" {
				problem(w, 403, "forbidden", "Access denied: your role cannot create resource")
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] == "taken" {
				problem(w, 409, "conflict", `An app named "taken" already exists in this organisation`)
				return
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(strings.Replace(strings.Replace(appJSON, `"name":"web"`, `"name":"`+body["name"].(string)+`"`, 1), `"status":"running"`, `"status":"deploying"`, 1)))
			return
		}
		_, _ = w.Write([]byte(`{"items":[` + appJSON + `]}`))
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/web", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		if r.Method == http.MethodDelete {
			if auth(r) == "viewer-token" {
				problem(w, 403, "forbidden", "Access denied: your role cannot delete resource")
				return
			}
			w.WriteHeader(204)
			return
		}
		_, _ = w.Write([]byte(appJSON))
	})
	mux.HandleFunc("/api/v1/orgs/acme/apps/", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		problem(w, 404, "not-found", "Application not found")
	})
	jsonType := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(jsonType)
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T, token string) {
	t.Helper()
	srv := fakePlatform(t)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	if token != "" {
		t.Setenv("CLOUD_TOKEN", token)
	} else {
		t.Setenv("CLOUD_TOKEN", "")
	}
}

func TestOrgList_Table(t *testing.T) {
	setup(t, "owner-token")
	out, err := run(t, "org", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "SLUG") || !strings.Contains(out, "acme") || !strings.Contains(out, "Beta Ltd") || !strings.Contains(out, "viewer") {
		t.Errorf("table missing rows:\n%s", out)
	}
}

func TestAppList_JSONIsFullResource(t *testing.T) {
	setup(t, "owner-token")
	out, err := run(t, "app", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var apps []map[string]any
	if err := json.Unmarshal([]byte(out), &apps); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if apps[0]["url"] != "https://web.apps.cloud.co.zm" || apps[0]["region"] != "zm-lusaka-central-1" {
		t.Errorf("json lost fields: %v", apps[0])
	}
}

func TestAppList_Quiet(t *testing.T) {
	setup(t, "owner-token")
	out, err := run(t, "app", "list", "-q")
	if err != nil || out != "web\n" {
		t.Errorf("quiet must print names only, got %q %v", out, err)
	}
}

func TestAppGet_ShowsRegionNameNotId(t *testing.T) {
	setup(t, "owner-token")
	out, _ := run(t, "app", "get", "web")
	if !strings.Contains(out, "zm-lusaka-central-1") || strings.Contains(out, "r1\n") {
		t.Errorf("must show the region name:\n%s", out)
	}
}

func TestAppGet_NotFound_Exit5(t *testing.T) {
	setup(t, "owner-token")
	_, err := run(t, "app", "get", "nope")
	if err == nil || ExitCode(err) != ExitMissing || !strings.Contains(err.Error(), "Application not found") {
		t.Errorf("want exit %d with the platform's sentence, got %d %v", ExitMissing, ExitCode(err), err)
	}
}

func TestAppDelete_ViewerForbidden_Exit4(t *testing.T) {
	setup(t, "viewer-token")
	_, err := run(t, "app", "delete", "web", "--yes")
	if err == nil || ExitCode(err) != ExitDenied {
		t.Errorf("want exit %d, got %d %v", ExitDenied, ExitCode(err), err)
	}
}

func TestAppDelete_RefusesWithoutConfirmationWhenNotATerminal(t *testing.T) {
	setup(t, "owner-token")
	_, err := run(t, "app", "delete", "web")
	if err == nil || ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("non-interactive delete without --yes must be a usage error naming --yes, got %v", err)
	}
}

func TestAppCreate_Conflict_Exit2(t *testing.T) {
	setup(t, "owner-token")
	t.Setenv("CLOUD_REGION", "zm-lusaka-central-1")
	_, err := run(t, "app", "create", "taken", "--image", "nginx")
	if err == nil || ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("want a usage exit with the conflict sentence, got %d %v", ExitCode(err), err)
	}
}

func TestAppCreate_NeedsRegion(t *testing.T) {
	setup(t, "owner-token")
	_, err := run(t, "app", "create", "new", "--image", "nginx")
	if err == nil || !strings.Contains(err.Error(), "--region") {
		t.Errorf("must explain how to pick a region, got %v", err)
	}
}

func TestAppCreate_PrintsURL(t *testing.T) {
	setup(t, "owner-token")
	out, err := run(t, "app", "create", "new", "--image", "nginx", "--region", "zm-lusaka-central-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "https://web.apps.cloud.co.zm" {
		t.Errorf("table mode prints the URL and nothing else on stdout, got %q", out)
	}
}

func TestNotSignedIn_Exit3(t *testing.T) {
	setup(t, "")
	_, err := run(t, "org", "list")
	if err == nil || ExitCode(err) != ExitAuth || !strings.Contains(err.Error(), "cloud login") {
		t.Errorf("want exit %d naming cloud login, got %d %v", ExitAuth, ExitCode(err), err)
	}
}

func TestRejectedToken_Exit3(t *testing.T) {
	setup(t, "garbage-token")
	_, err := run(t, "org", "list")
	if err == nil || ExitCode(err) != ExitAuth {
		t.Errorf("a rejected token is an auth failure, got %d %v", ExitCode(err), err)
	}
}

func TestNoOrg_UsageHint(t *testing.T) {
	setup(t, "owner-token")
	t.Setenv("CLOUD_ORG", "")
	_, err := run(t, "app", "list")
	if err == nil || ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "cloud org use") {
		t.Errorf("must tell the user how to pick an organisation, got %v", err)
	}
}

func TestLogin_TokenStdin_VerifiesAndStores(t *testing.T) {
	setup(t, "")
	testStdin = strings.NewReader("owner-token\n")
	if _, err := run(t, "login", "--token-stdin"); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	// Now authenticated from the stored credential, with no CLOUD_TOKEN.
	out, err := run(t, "whoami", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"email": "a@b.zm"`) || !strings.Contains(out, `"authKind": "api-token"`) {
		t.Errorf("whoami after login wrong:\n%s", out)
	}
	if _, err := run(t, "logout"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "whoami"); err == nil || ExitCode(err) != ExitAuth {
		t.Errorf("after logout, whoami must be exit %d, got %v", ExitAuth, err)
	}
}

func TestLogin_TokenStdin_BadTokenNotStored(t *testing.T) {
	setup(t, "")
	testStdin = strings.NewReader("bad-token\n")
	if _, err := run(t, "login", "--token-stdin"); err == nil || ExitCode(err) != ExitAuth {
		t.Fatalf("a rejected token must fail login with exit %d, got %v", ExitAuth, err)
	}
	if _, err := run(t, "whoami"); err == nil {
		t.Error("nothing must have been stored")
	}
}

func TestParseEnv(t *testing.T) {
	m, err := parseEnv([]string{"A=1", "B=x=y", "EMPTY="})
	if err != nil || m["A"] != "1" || m["B"] != "x=y" || m["EMPTY"] != "" {
		t.Errorf("got %v %v", m, err)
	}
	if _, err := parseEnv([]string{"NOVALUE"}); err == nil {
		t.Error("bare KEY must be rejected")
	}
}
