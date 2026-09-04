package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestErrorFromResponse_ProblemJSON(t *testing.T) {
	e := ErrorFromResponse(403, []byte(`{"type":"https://cloud.co.zm/api/v1/problems/forbidden","title":"forbidden","status":403,"detail":"Access denied: your role cannot delete resource"}`))
	if e.Type != "forbidden" || e.Status != 403 {
		t.Errorf("wrong type/status: %+v", e)
	}
	if e.Error() != "Access denied: your role cannot delete resource" {
		t.Errorf("Error() must be the platform's detail, got %q", e.Error())
	}
	if !errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) {
		t.Error("errors.Is must match on type")
	}
}

func TestErrorFromResponse_NonJSON(t *testing.T) {
	e := ErrorFromResponse(502, []byte("<html>Bad gateway</html>"))
	if e.Type != "internal" || e.Status != 502 {
		t.Errorf("%+v", e)
	}
	if e.Error() != "the platform returned HTTP 502" {
		t.Errorf("html bodies must not be echoed: %q", e.Error())
	}
	e = ErrorFromResponse(404, []byte("route not found"))
	if e.Type != "not-found" || e.Error() != "the platform returned HTTP 404: route not found" {
		t.Errorf("%+v", e)
	}
}

func TestNew_AttachesAuthAndUserAgent(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","api":"v1","database":"ok","worker":{"status":"ok","lastHeartbeat":null,"ageSeconds":3},"time":"2026-09-04T00:00:00Z"}`))
	}))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL, Token: "tok-1", UserAgent: "swiftcloud-cli/test"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.GetHealthWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(res.HTTPResponse, res.Body); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer tok-1" {
		t.Errorf("Authorization = %q", got.Get("Authorization"))
	}
	if got.Get("User-Agent") != "swiftcloud-cli/test" {
		t.Errorf("User-Agent = %q", got.Get("User-Agent"))
	}
	if res.JSON200 == nil || string(res.JSON200.Status) != "ok" {
		t.Errorf("typed body not decoded: %+v", res.JSON200)
	}
}

func TestNew_NoTokenSendsNoAuthorization(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"type":"https://cloud.co.zm/api/v1/problems/unauthenticated","title":"unauthenticated","status":401,"detail":"Sign in"}`))
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL})
	res, _ := c.GetMeWithResponse(context.Background())
	if got.Get("Authorization") != "" {
		t.Error("must not send an empty bearer")
	}
	err := Check(res.HTTPResponse, res.Body)
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("want unauthenticated, got %v", err)
	}
}
