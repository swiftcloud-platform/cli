package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud/internal/termsafe"
)

/*
Device-code login (RFC 8628), against better-auth's device-authorization plugin.

The CLI asks for a code, shows the person a short user code and a URL, then
polls for the token while they approve it in a browser that is already signed
in. No password ever passes through the terminal, and the same flow works over
SSH: the URL can be opened on any device.

The platform answers these under its auth base — `/api/auth/device/...` — not
under `/api/v1`; BaseFromAPI derives one from the other so a CLI pointed at
staging also logs in against staging.

Everything in the platform's answer is untrusted until checked: the
verification URL is opened in a browser and shown to a person as the place to
approve a login, so it must point at the API's own site (VerificationURL), and
the timing fields bound how long the CLI waits and how hard it polls.
*/

// ClientID is what the platform's validateClient accepts.
const ClientID = "cloud-cli"

const grantType = "urn:ietf:params:oauth:grant-type:device_code"

// Bounds on what the server may ask of the poll loop.
const (
	// DefaultExpiry is used when the server sends no usable expires_in. The
	// platform's own setting is 10 minutes.
	DefaultExpiry = 15 * time.Minute
	// MaxExpiry caps how long a login waits, whatever the server says.
	MaxExpiry = 30 * time.Minute
	// MaxInterval caps the poll interval, including after slow_down.
	MaxInterval = 60 * time.Second
)

// BaseFromAPI maps https://host/api/v1 → https://host/api/auth.
func BaseFromAPI(apiURL string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid API URL %q", apiURL)
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1") + "/auth"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// DeviceCode is the platform's answer to a login request.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Expiry is how long to wait for approval: the server's expires_in, or
// DefaultExpiry when it sent none, never more than MaxExpiry.
func (dc *DeviceCode) Expiry() time.Duration {
	if dc.ExpiresIn <= 0 {
		return DefaultExpiry
	}
	d := time.Duration(dc.ExpiresIn) * time.Second
	if d > MaxExpiry {
		return MaxExpiry
	}
	return d
}

// Token is the result of a successful poll.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// Terminal outcomes of a poll, as the RFC names them.
var (
	ErrAccessDenied = errors.New("the request was denied in the browser")
	ErrExpired      = errors.New("the code expired before it was approved — run `cloud login` again")
	ErrInvalidGrant = errors.New("the platform did not recognise this login request")
)

// DeviceFlow talks to one auth base.
type DeviceFlow struct {
	AuthBase  string
	UserAgent string
	HTTP      *http.Client
	// Sleep is injectable so tests do not wait.
	Sleep func(time.Duration)
}

func (d *DeviceFlow) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirects}
}

// noRedirects keeps the device flow on the host it was pointed at: a
// redirect would replay the device code (or a session token) elsewhere.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (d *DeviceFlow) sleep(dur time.Duration) {
	if d.Sleep != nil {
		d.Sleep(dur)
		return
	}
	time.Sleep(dur)
}

func (d *DeviceFlow) post(ctx context.Context, path, bearer string, body any) (int, []byte, error) {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.AuthBase+path, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := d.http().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 && res.StatusCode <= 399 {
		return res.StatusCode, nil, fmt.Errorf("the platform redirected %s to %s; the CLI does not follow redirects — check the API URL",
			d.AuthBase+path, termsafe.String(res.Header.Get("Location")))
	}
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, data, nil
}

// Start requests a device code.
func (d *DeviceFlow) Start(ctx context.Context) (*DeviceCode, error) {
	status, data, err := d.post(ctx, "/device/code", "", map[string]string{"client_id": ClientID})
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", d.AuthBase, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("login request refused (HTTP %d): %s", status, oneLine(data))
	}
	var dc DeviceCode
	if err := json.Unmarshal(data, &dc); err != nil || dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, errors.New("login response was not understood — is the API URL pointing at a SwiftCloud platform?")
	}
	if dc.Interval <= 0 {
		dc.Interval = 5
	}
	if dc.Interval > int(MaxInterval/time.Second) {
		dc.Interval = int(MaxInterval / time.Second)
	}
	return &dc, nil
}

// VerificationURL picks the URL to show and open — verification_uri_complete,
// else verification_uri — and refuses it unless it is plainly the platform's
// own page. The person is told to approve a login there; a URL on another
// site is a phishing page with the platform's blessing.
//
// Accepted: https (http only when the host is loopback), no user:password@,
// and a host equal to the API host, under the API host, or under the API
// host's parent when that parent still has three or more labels (so
// api.cloud.co.zm may send people to cloud.co.zm, but cloud.co.zm may not
// send them to anything under co.zm).
func VerificationURL(dc *DeviceCode, apiURL string) (string, error) {
	api, err := url.Parse(apiURL)
	if err != nil || api.Host == "" {
		return "", fmt.Errorf("invalid API URL %q", apiURL)
	}
	raw := dc.VerificationURIComplete
	if raw == "" {
		raw = dc.VerificationURI
	}
	if raw == "" {
		return "", errors.New("the platform did not say where to approve this login")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the platform sent an approval URL that is not a URL: %q", termsafe.String(raw))
	}
	host := normHost(u.Hostname())
	apiHost := normHost(api.Hostname())
	switch {
	case u.User != nil:
		return "", fmt.Errorf("refusing approval URL %q: it contains a user name", termsafe.String(raw))
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(host):
	default:
		return "", fmt.Errorf("refusing approval URL %q: it must use https", termsafe.String(raw))
	}
	if !hostAllowed(host, apiHost) {
		return "", fmt.Errorf("refusing approval URL %q: it is not on %s, the API you are signing in to", termsafe.String(raw), apiHost)
	}
	return u.String(), nil
}

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func hostAllowed(host, apiHost string) bool {
	if host == "" || apiHost == "" {
		return false
	}
	if host == apiHost || strings.HasSuffix(host, "."+apiHost) {
		return true
	}
	if net.ParseIP(apiHost) != nil {
		return false
	}
	i := strings.IndexByte(apiHost, '.')
	if i < 0 {
		return false
	}
	parent := apiHost[i+1:]
	if strings.Count(parent, ".") < 2 {
		return false
	}
	return host == parent || strings.HasSuffix(host, "."+parent)
}

type tokenError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// Poll exchanges the device code for a token, waiting as the server directs.
// It returns when approved, denied, expired, or ctx is done.
func (d *DeviceFlow) Poll(ctx context.Context, dc *DeviceCode) (*Token, error) {
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if interval > MaxInterval {
		interval = MaxInterval
	}
	deadline := time.Now().Add(dc.Expiry())
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, ErrExpired
		}
		status, data, err := d.post(ctx, "/device/token", "", map[string]string{
			"grant_type":  grantType,
			"device_code": dc.DeviceCode,
			"client_id":   ClientID,
		})
		if err != nil {
			return nil, fmt.Errorf("could not reach %s: %w", d.AuthBase, err)
		}
		if status == http.StatusOK {
			var t Token
			if err := json.Unmarshal(data, &t); err != nil || t.AccessToken == "" {
				return nil, errors.New("token response was not understood")
			}
			return &t, nil
		}
		var te tokenError
		_ = json.Unmarshal(data, &te)
		switch te.Error {
		case "authorization_pending":
			// keep waiting
		case "slow_down":
			interval += 5 * time.Second
			if interval > MaxInterval {
				interval = MaxInterval
			}
		case "access_denied":
			return nil, ErrAccessDenied
		case "expired_token":
			return nil, ErrExpired
		case "invalid_grant":
			return nil, ErrInvalidGrant
		default:
			return nil, fmt.Errorf("login failed (HTTP %d): %s", status, oneLine(data))
		}
		d.sleep(interval)
	}
}

// SignOut asks the platform to end the session behind token — better-auth's
// POST /sign-out, which the bearer plugin accepts with the session token in
// Authorization. Only session tokens from `cloud login` can be ended this
// way; an `sc_` API token is revoked in the dashboard.
func (d *DeviceFlow) SignOut(ctx context.Context, token string) error {
	status, data, err := d.post(ctx, "/sign-out", token, map[string]string{})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", status, oneLine(data))
	}
	return nil
}

func oneLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return termsafe.String(strings.ReplaceAll(s, "\n", " "))
}
