package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBaseFromAPI(t *testing.T) {
	for in, want := range map[string]string{
		"https://cloud.co.zm/api/v1":           "https://cloud.co.zm/api/auth",
		"https://cloud.co.zm/api/v1/":          "https://cloud.co.zm/api/auth",
		"http://localhost:5173/api/v1":         "http://localhost:5173/api/auth",
		"https://staging.cloud.co.zm/api/v1?x": "https://staging.cloud.co.zm/api/auth",
	} {
		got, err := BaseFromAPI(in)
		if err != nil || got != want {
			t.Errorf("BaseFromAPI(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := BaseFromAPI("not a url"); err == nil {
		t.Error("garbage must be rejected")
	}
}

// A fake better-auth that approves after N polls, or ends with a given error.
func fakeAuth(t *testing.T, approveAfter int, terminal string) (*httptest.Server, *int32) {
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/code", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != ClientID {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "dev-123", UserCode: "ABCD1234", VerificationURI: "http://x/device", VerificationURIComplete: "http://x/device?user_code=ABCD1234", ExpiresIn: 600, Interval: 1})
	})
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != grantType || body["device_code"] != "dev-123" || body["client_id"] != ClientID {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		n := atomic.AddInt32(&polls, 1)
		if int(n) <= approveAfter {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		if terminal != "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"` + terminal + `"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(Token{AccessToken: "session-token-xyz", TokenType: "Bearer", ExpiresIn: 604800})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &polls
}

func flow(srv *httptest.Server) *DeviceFlow {
	return &DeviceFlow{AuthBase: srv.URL + "/api/auth", Sleep: func(time.Duration) {}}
}

func TestDeviceFlow_HappyPath(t *testing.T) {
	srv, polls := fakeAuth(t, 2, "")
	f := flow(srv)
	dc, err := f.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "ABCD1234" || dc.VerificationURIComplete == "" {
		t.Errorf("start response wrong: %+v", dc)
	}
	tok, err := f.Poll(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "session-token-xyz" || tok.ExpiresIn != 604800 {
		t.Errorf("token wrong: %+v", tok)
	}
	if *polls != 3 {
		t.Errorf("want 3 polls (2 pending + 1 approved), got %d", *polls)
	}
}

func TestDeviceFlow_Denied(t *testing.T) {
	srv, _ := fakeAuth(t, 0, "access_denied")
	f := flow(srv)
	dc, _ := f.Start(context.Background())
	_, err := f.Poll(context.Background(), dc)
	if !errors.Is(err, ErrAccessDenied) {
		t.Errorf("want ErrAccessDenied, got %v", err)
	}
}

func TestDeviceFlow_Expired(t *testing.T) {
	srv, _ := fakeAuth(t, 0, "expired_token")
	f := flow(srv)
	dc, _ := f.Start(context.Background())
	_, err := f.Poll(context.Background(), dc)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("want ErrExpired, got %v", err)
	}
}

func TestDeviceFlow_SlowDownBacksOff(t *testing.T) {
	var slept []time.Duration
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		n++
		switch n {
		case 1:
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		case 2:
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"ok","token_type":"Bearer","expires_in":10}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := &DeviceFlow{AuthBase: srv.URL + "/api/auth", Sleep: func(d time.Duration) { slept = append(slept, d) }}
	tok, err := f.Poll(context.Background(), &DeviceCode{DeviceCode: "d", Interval: 5, ExpiresIn: 600})
	if err != nil || tok.AccessToken != "ok" {
		t.Fatalf("got %v %v", tok, err)
	}
	if len(slept) != 2 {
		t.Fatalf("want 2 sleeps (after slow_down and after pending), got %v", slept)
	}
	if slept[0] != 10*time.Second || slept[1] != 10*time.Second {
		t.Errorf("slow_down must add 5s to the 5s interval and keep it: %v", slept)
	}
}

func TestDeviceFlow_ContextCancelStopsPolling(t *testing.T) {
	srv, _ := fakeAuth(t, 1000, "")
	f := flow(srv)
	dc, _ := f.Start(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	f.Sleep = func(time.Duration) { cancel() }
	_, err := f.Poll(ctx, dc)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestDeviceFlow_StartAgainstNonPlatform(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not an api</html>`))
	}))
	defer srv.Close()
	_, err := (&DeviceFlow{AuthBase: srv.URL}).Start(context.Background())
	if err == nil || !contains(err.Error(), "SwiftCloud") {
		t.Errorf("want a hint about the API URL, got %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
