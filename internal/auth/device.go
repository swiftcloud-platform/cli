package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
*/

// ClientID is what the platform's validateClient accepts.
const ClientID = "cloud-cli"

const grantType = "urn:ietf:params:oauth:grant-type:device_code"

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
	AuthBase string
	HTTP     *http.Client
	// Sleep is injectable so tests do not wait.
	Sleep func(time.Duration)
}

func (d *DeviceFlow) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (d *DeviceFlow) sleep(dur time.Duration) {
	if d.Sleep != nil {
		d.Sleep(dur)
		return
	}
	time.Sleep(dur)
}

func (d *DeviceFlow) post(ctx context.Context, path string, body any) (int, []byte, error) {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.AuthBase+path, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	res, err := d.http().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, data, nil
}

// Start requests a device code.
func (d *DeviceFlow) Start(ctx context.Context) (*DeviceCode, error) {
	status, data, err := d.post(ctx, "/device/code", map[string]string{"client_id": ClientID})
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
	return &dc, nil
}

type tokenError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// Poll exchanges the device code for a token, waiting as the server directs.
// It returns when approved, denied, expired, or ctx is done.
func (d *DeviceFlow) Poll(ctx context.Context, dc *DeviceCode) (*Token, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if dc.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, ErrExpired
		}
		status, data, err := d.post(ctx, "/device/token", map[string]string{
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

func oneLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.ReplaceAll(s, "\n", " ")
}
