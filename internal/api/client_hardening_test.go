package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The finding this guards: `cloud vm ssh-key remove web ..` built
// DELETE /orgs/acme/vms/web/ssh-keys/.. which URL resolution collapsed to
// DELETE /orgs/acme/vms/web/; the platform 308'd the trailing slash and Go
// followed with DELETE /orgs/acme/vms/web, deleting the VM.
func TestNew_RefusesACollapsedPathAndNeverSendsIt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL + "/api/v1", Token: "t"})
	for _, args := range [][3]string{
		{"acme", "web", ".."},
		{"acme", "web", "."},
		{"acme", "web", ""},
		{"acme", "", "k1"},
		{"", "web", "k1"},
	} {
		_, err := c.DeleteOrgsOrgVmsVmSshKeysKeyIdWithResponse(context.Background(), args[0], args[1], args[2])
		var pe *PathError
		if !errors.As(err, &pe) {
			t.Errorf("args %q: want a PathError, got %v", args, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests reached the server; none should", hits.Load())
	}
}

func TestCheckRequestPath(t *testing.T) {
	ok := []string{"/api/v1/orgs/acme/apps", "/api/v1/orgs/acme/apps/web%2Fx/logs", "/", ""}
	bad := []string{"/api/v1/orgs/acme/vms/web/", "/api/v1/orgs//apps", "/api/v1/orgs/acme/apps/./x", "/api/v1/../x"}
	for _, p := range ok {
		u := &url.URL{Path: p}
		if strings.Contains(p, "%2F") {
			u, _ = url.Parse(p)
		}
		if err := CheckRequestPath(u); err != nil {
			t.Errorf("%q: unexpected %v", p, err)
		}
	}
	for _, p := range bad {
		if err := CheckRequestPath(&url.URL{Path: p}); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
}

func TestNew_RefusesRedirectsAndNamesTheLocation(t *testing.T) {
	var deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			deletes.Add(1)
			w.WriteHeader(204)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusPermanentRedirect)
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL + "/api/v1", Token: "t"})
	_, err := c.DeleteOrgsOrgVmsVmSshKeysKeyIdWithResponse(context.Background(), "acme", "web", "k1")
	var re *RedirectError
	if !errors.As(err, &re) {
		t.Fatalf("want RedirectError, got %v", err)
	}
	if re.Status != 308 || re.Location != "/elsewhere" || re.Method != http.MethodDelete {
		t.Errorf("%+v", re)
	}
	if !strings.Contains(err.Error(), "/elsewhere") {
		t.Errorf("message must name the Location: %q", err)
	}
	if deletes.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestNew_CapsTheResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"items":[` + strings.Repeat(" ", 4096) + `]}`))
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, MaxResponseBytes: 1024})
	_, err := c.GetOrgsWithResponse(context.Background(), nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}

	// A body exactly at the cap is fine.
	body := `{"items":[]}`
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv2.Close()
	c2, _ := New(Options{BaseURL: srv2.URL, MaxResponseBytes: int64(len(body))})
	if _, err := c2.GetOrgsWithResponse(context.Background(), nil); err != nil {
		t.Fatalf("a body at the cap must be read: %v", err)
	}
}

// `app logs -f` died after 60 s because http.Client.Timeout covers reading
// the body. A followed stream must outlive the ordinary request timeout.
func TestNew_FollowedLogsOutliveTheRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for i := 0; i < 3; i++ {
			time.Sleep(60 * time.Millisecond)
			_, _ = io.WriteString(w, "line\n")
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Timeout: 100 * time.Millisecond})

	follow := true
	res, err := c.GetOrgsOrgAppsAppLogs(context.Background(), "acme", "web", &GetOrgsOrgAppsAppLogsParams{Follow: &follow})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil || strings.Count(string(data), "line") != 3 {
		t.Fatalf("followed stream cut short: %q, %v", data, err)
	}

	// An ordinary request is still bounded.
	res, err = c.GetOrgsOrgAppsAppLogs(context.Background(), "acme", "web", nil)
	if err == nil {
		_, err = io.ReadAll(res.Body)
		_ = res.Body.Close()
	}
	if err == nil {
		t.Fatal("a non-streaming request must still time out")
	}
}

func TestErrorFromResponse_StripsControlCharacters(t *testing.T) {
	e := ErrorFromResponse(400, []byte(`{"type":"https://x/problems/validation","detail":"bad \u001b[2Jname\u001b]0;pwned\u0007"}`))
	if strings.ContainsAny(e.Error(), "\x1b\x07") {
		t.Errorf("escape characters survived: %q", e.Error())
	}
	e = ErrorFromResponse(500, []byte("oops \x1b[31mred"))
	if strings.Contains(e.Error(), "\x1b") {
		t.Errorf("escape characters survived: %q", e.Error())
	}
}
