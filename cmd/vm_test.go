package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
VM command tests against a fake platform. They check what a person sees: the
table, the JSON, the one-line quiet output, the sentence on failure, and the
exit code a script would branch on. The fake speaks the real wire shapes.
*/

const vmJSON = `{"id":"vm1","name":"web","organizationId":"org1","region":"zm-lusaka-central-1","regionId":"r1","image":"ubuntu-24.04","size":"vm-1","description":"test VM","status":"running","publicIp":"203.0.113.10","privateIp":"10.0.0.5","errorMessage":"","createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`

const vmSSHKeyJSON = `{"id":"key1","label":"laptop","publicKey":"ssh-ed25519 AAAA...","fingerprint":"SHA256:abc123","addedAt":"2026-09-01T00:00:00Z"}`

func fakeVMPlatform(t *testing.T) *httptest.Server {
	t.Helper()
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
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","api":"v1","database":"ok","worker":{"status":"ok","lastHeartbeat":"2026-09-04T00:00:00Z","ageSeconds":2},"time":"2026-09-04T00:00:00Z"}`))
	})

	// VM list
	mux.HandleFunc("/api/v1/orgs/acme/vms", func(w http.ResponseWriter, r *http.Request) {
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
				problem(w, 409, "conflict", `A VM named "taken" already exists in this organisation`)
				return
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(strings.Replace(vmJSON, `"name":"web"`, `"name":"`+body["name"].(string)+`"`, 1)))
			return
		}
		_, _ = w.Write([]byte(`{"items":[` + vmJSON + `]}`))
	})

	// VM item + power + ssh-keys + ip
	handleVM := func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodDelete:
			if auth(r) == "viewer-token" {
				problem(w, 403, "forbidden", "Access denied: your role cannot delete resource")
				return
			}
			w.WriteHeader(204)
			return
		case r.URL.Path == "/api/v1/orgs/acme/vms/web/ssh-keys":
			if r.Method == http.MethodPost {
				if auth(r) == "viewer-token" {
					problem(w, 403, "forbidden", "Access denied: your role cannot update resource")
					return
				}
				w.WriteHeader(201)
				_, _ = w.Write([]byte(vmSSHKeyJSON))
				return
			}
			_, _ = w.Write([]byte(`{"items":[` + vmSSHKeyJSON + `]}`))
			return
		case strings.HasPrefix(r.URL.Path, "/api/v1/orgs/acme/vms/web/ssh-keys/"):
			if r.Method == http.MethodDelete {
				if auth(r) == "viewer-token" {
					problem(w, 403, "forbidden", "Access denied: your role cannot update resource")
					return
				}
				w.WriteHeader(204)
				return
			}
		case r.URL.Path == "/api/v1/orgs/acme/vms/web/ip":
			_, _ = w.Write([]byte(`{"publicIp":"203.0.113.10","privateIp":"10.0.0.5"}`))
			return
		case r.URL.Path == "/api/v1/orgs/acme/vms/web/ip/attach":
			if auth(r) == "viewer-token" {
				problem(w, 403, "forbidden", "Access denied: your role cannot update resource")
				return
			}
			_, _ = w.Write([]byte(`{"publicIp":"203.0.113.20","privateIp":"10.0.0.5"}`))
			return
		case r.URL.Path == "/api/v1/orgs/acme/vms/web/ip/detach":
			if auth(r) == "viewer-token" {
				problem(w, 403, "forbidden", "Access denied: your role cannot update resource")
				return
			}
			_, _ = w.Write([]byte(`{"publicIp":"","privateIp":"10.0.0.5"}`))
			return
		case strings.HasSuffix(r.URL.Path, "/start") || strings.HasSuffix(r.URL.Path, "/stop") || strings.HasSuffix(r.URL.Path, "/restart"):
			if auth(r) == "viewer-token" {
				problem(w, 403, "forbidden", "Access denied: your role cannot update resource")
				return
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte(vmJSON))
			return
		}
		_, _ = w.Write([]byte(vmJSON))
	}
	mux.HandleFunc("/api/v1/orgs/acme/vms/web", handleVM)
	mux.HandleFunc("/api/v1/orgs/acme/vms/web/", handleVM)
	mux.HandleFunc("/api/v1/orgs/acme/vms/", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		problem(w, 404, "not-found", "VM not found")
	})

	jsonType := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(jsonType)
	t.Cleanup(srv.Close)
	return srv
}

func vmSetup(t *testing.T, token string) {
	t.Helper()
	srv := fakeVMPlatform(t)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	if token != "" {
		t.Setenv("CLOUD_TOKEN", token)
	} else {
		t.Setenv("CLOUD_TOKEN", "")
	}
}

func TestVMList_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	out, err := run(t, "vm", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "web") || !strings.Contains(out, "running") || !strings.Contains(out, "203.0.113.10") {
		t.Errorf("table missing rows:\n%s", out)
	}
}

func TestVMList_JSON(t *testing.T) {
	vmSetup(t, "owner-token")
	out, err := run(t, "vm", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var vms []map[string]any
	if err := json.Unmarshal([]byte(out), &vms); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if vms[0]["name"] != "web" || vms[0]["image"] != "ubuntu-24.04" {
		t.Errorf("json lost fields: %v", vms[0])
	}
}

func TestVMList_Quiet(t *testing.T) {
	vmSetup(t, "owner-token")
	out, err := run(t, "vm", "list", "-q")
	if err != nil || out != "web\n" {
		t.Errorf("quiet must print names only, got %q %v", out, err)
	}
}

func TestVMGet_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	out, err := run(t, "vm", "get", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "running") || !strings.Contains(out, "ubuntu-24.04") || !strings.Contains(out, "203.0.113.10") {
		t.Errorf("table missing fields:\n%s", out)
	}
}

func TestVMGet_NotFound_Exit5(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "get", "nope")
	if err == nil || ExitCode(err) != ExitMissing || !strings.Contains(err.Error(), "VM not found") {
		t.Errorf("want exit %d with the platform's sentence, got %d %v", ExitMissing, ExitCode(err), err)
	}
}

func TestVMCreate_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	t.Setenv("CLOUD_REGION", "zm-lusaka-central-1")
	out, err := run(t, "vm", "create", "newvm", "--image", "ubuntu-24.04")
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		// Table mode prints status to stderr; just verify no error.
		return
	}
}

func TestVMCreate_Conflict_Exit2(t *testing.T) {
	vmSetup(t, "owner-token")
	t.Setenv("CLOUD_REGION", "zm-lusaka-central-1")
	_, err := run(t, "vm", "create", "taken", "--image", "ubuntu-24.04")
	if err == nil || ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("want a usage exit with the conflict sentence, got %d %v", ExitCode(err), err)
	}
}

func TestVMCreate_NeedsRegion(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "create", "newvm", "--image", "ubuntu-24.04")
	if err == nil || !strings.Contains(err.Error(), "--region") {
		t.Errorf("must explain how to pick a region, got %v", err)
	}
}

func TestVMCreate_ViewerForbidden_Exit4(t *testing.T) {
	vmSetup(t, "viewer-token")
	t.Setenv("CLOUD_REGION", "zm-lusaka-central-1")
	_, err := run(t, "vm", "create", "newvm", "--image", "ubuntu-24.04")
	if err == nil || ExitCode(err) != ExitDenied {
		t.Errorf("want exit %d, got %d %v", ExitDenied, ExitCode(err), err)
	}
}

func TestVMDelete_ViewerForbidden_Exit4(t *testing.T) {
	vmSetup(t, "viewer-token")
	_, err := run(t, "vm", "delete", "web", "--yes")
	if err == nil || ExitCode(err) != ExitDenied {
		t.Errorf("want exit %d, got %d %v", ExitDenied, ExitCode(err), err)
	}
}

func TestVMDelete_RefusesWithoutConfirmation(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "delete", "web")
	if err == nil || ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("non-interactive delete without --yes must be a usage error naming --yes, got %v", err)
	}
}

func TestVMStart_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "start", "web")
	if err != nil {
		t.Fatal(err)
	}
}

func TestVMStop_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "stop", "web")
	if err != nil {
		t.Fatal(err)
	}
}

func TestVMRestart_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "restart", "web")
	if err != nil {
		t.Fatal(err)
	}
}

func TestVMSshKeyList_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	out, err := run(t, "vm", "ssh-key", "list", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "key1") || !strings.Contains(out, "laptop") || !strings.Contains(out, "SHA256:abc123") {
		t.Errorf("table missing key rows:\n%s", out)
	}
}

func TestVMSshKeyAdd_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "ssh-key", "add", "web", "ssh-ed25519 AAAA...", "--label", "test")
	if err != nil {
		t.Fatal(err)
	}
}

func TestVMIPGet_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	out, err := run(t, "vm", "ip", "get", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "203.0.113.10") || !strings.Contains(out, "10.0.0.5") {
		t.Errorf("table missing IP info:\n%s", out)
	}
}

func TestVMIPAttach_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "ip", "attach", "web")
	if err != nil {
		t.Fatal(err)
	}
}

func TestVMIPDetach_Table(t *testing.T) {
	vmSetup(t, "owner-token")
	_, err := run(t, "vm", "ip", "detach", "web", "--yes")
	if err != nil {
		t.Fatal(err)
	}
}
