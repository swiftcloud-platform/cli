package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerificationURL(t *testing.T) {
	ok := map[string]string{ // approval URL → API URL
		"https://cloud.co.zm/device?user_code=ABCD1234":            "https://cloud.co.zm/api/v1",
		"https://CLOUD.co.zm./device":                              "https://cloud.co.zm/api/v1",
		"https://auth.cloud.co.zm/device":                          "https://cloud.co.zm/api/v1",
		"https://cloud.co.zm/device":                               "https://api.cloud.co.zm/api/v1",
		"https://dash.cloud.co.zm/device":                          "https://api.cloud.co.zm/api/v1",
		"http://localhost:5173/device?user_code=X":                 "http://localhost:5173/api/v1",
		"http://127.0.0.1:5173/device":                             "http://127.0.0.1:5173/api/v1",
		"https://staging.cloud.co.zm/device?user_code=ABCD-1234":   "https://staging.cloud.co.zm/api/v1",
		"https://staging.cloud.co.zm:8443/device?user_code=ABCD12": "https://staging.cloud.co.zm:8443/api/v1",
	}
	for raw, api := range ok {
		if _, err := VerificationURL(&DeviceCode{VerificationURIComplete: raw}, api); err != nil {
			t.Errorf("%s (API %s) should be accepted: %v", raw, api, err)
		}
	}
	bad := map[string]string{
		"https://evil.example/device":                 "https://cloud.co.zm/api/v1",
		"https://cloud.co.zm.evil.example/device":     "https://cloud.co.zm/api/v1",
		"https://evilcloud.co.zm/device":              "https://cloud.co.zm/api/v1",
		"https://other.co.zm/device":                  "https://cloud.co.zm/api/v1", // co.zm is a public suffix
		"http://cloud.co.zm/device":                   "https://cloud.co.zm/api/v1",
		"https://cloud.co.zm@evil.example/device":     "https://cloud.co.zm/api/v1",
		"javascript:alert(1)":                         "https://cloud.co.zm/api/v1",
		"file:///etc/passwd":                          "https://cloud.co.zm/api/v1",
		"https://example.com/device":                  "https://api.example.com/api/v1",
		"http://app.localhost/device":                 "http://localhost:5173/api/v1",
		"https://cloud.co.zm/device\x1b[2J":           "https://cloud.co.zm/api/v1x",
		"":                                            "https://cloud.co.zm/api/v1",
		"https://127.0.0.2/device":                    "https://127.0.0.1/api/v1",
		"https://notcloud.co.zm/device?user_code=XYZ": "https://cloud.co.zm/api/v1",
	}
	for raw, api := range bad {
		got, err := VerificationURL(&DeviceCode{VerificationURIComplete: raw}, api)
		if err == nil {
			t.Errorf("%q (API %s) must be refused, got %q", raw, api, got)
			continue
		}
		if strings.ContainsAny(err.Error(), "\x1b") {
			t.Errorf("error echoes control characters: %q", err)
		}
	}
	// verification_uri is the fallback when the complete form is absent.
	got, err := VerificationURL(&DeviceCode{VerificationURI: "https://cloud.co.zm/device"}, "https://cloud.co.zm/api/v1")
	if err != nil || got != "https://cloud.co.zm/device" {
		t.Errorf("fallback: %q %v", got, err)
	}
}

func TestDeviceCode_Expiry(t *testing.T) {
	for in, want := range map[int]time.Duration{0: DefaultExpiry, -5: DefaultExpiry, 600: 10 * time.Minute, 1 << 30: MaxExpiry} {
		if got := (&DeviceCode{ExpiresIn: in}).Expiry(); got != want {
			t.Errorf("Expiry(%d) = %s, want %s", in, got, want)
		}
	}
}

// A hostile server must not be able to park the CLI between polls for hours.
func TestDeviceFlow_IntervalIsCapped(t *testing.T) {
	var slept []time.Duration
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/device/code") {
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"U","interval":86400,"expires_in":0}`))
			return
		}
		n++
		if n < 4 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"ok"}`))
	}))
	defer srv.Close()
	f := &DeviceFlow{AuthBase: srv.URL, Sleep: func(d time.Duration) { slept = append(slept, d) }}
	dc, err := f.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.Interval != int(MaxInterval/time.Second) {
		t.Errorf("interval not capped at start: %d", dc.Interval)
	}
	// expires_in 0 must not mean "already expired".
	if _, err := f.Poll(context.Background(), dc); err != nil {
		t.Fatal(err)
	}
	for _, d := range slept {
		if d > MaxInterval {
			t.Errorf("slept %s, over the cap", d)
		}
	}
}

func TestDeviceFlow_SetsUserAgentAndRefusesRedirects(t *testing.T) {
	var ua atomic.Value
	var followed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Add(1)
			return
		}
		ua.Store(r.Header.Get("User-Agent"))
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	f := &DeviceFlow{AuthBase: srv.URL, UserAgent: "swiftcloud-cli/test"}
	_, err := f.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "/elsewhere") {
		t.Errorf("want a redirect refusal naming the Location, got %v", err)
	}
	if followed.Load() != 0 {
		t.Error("the redirect was followed")
	}
	if ua.Load() != "swiftcloud-cli/test" {
		t.Errorf("User-Agent = %v", ua.Load())
	}
}

func TestDeviceFlow_SignOutSendsTheBearer(t *testing.T) {
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	if err := (&DeviceFlow{AuthBase: srv.URL + "/api/auth"}).SignOut(context.Background(), "sess"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sess" || path != "/api/auth/sign-out" {
		t.Errorf("auth %q path %q", auth, path)
	}
}

func TestOneLine_StripsControlCharacters(t *testing.T) {
	if got := oneLine([]byte("bad\x1b[2J\nthing")); strings.ContainsAny(got, "\x1b\n") {
		t.Errorf("oneLine = %q", got)
	}
}
