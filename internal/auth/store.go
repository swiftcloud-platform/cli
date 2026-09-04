// Package auth holds the credential the CLI presents and how it was obtained.
//
// Two kinds of credential exist and both travel as `Authorization: Bearer`:
//
//   - a session token from `cloud login` (better-auth device flow), which the
//     platform expires after seven days, sliding daily;
//   - an `sc_` API token minted in the dashboard for CI, bound to one
//     organisation and one role.
//
// The stored credential is keyed by API host, so a staging login and a
// production login coexist. CLOUD_TOKEN in the environment overrides any
// stored credential — that is the CI path, where nothing is written to disk.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kind says where a credential came from; it changes what `whoami` says and
// how expiry is described.
type Kind string

const (
	KindSession  Kind = "session"
	KindAPIToken Kind = "api-token"
	KindEnv      Kind = "env"
)

// Credential is what gets attached to requests.
type Credential struct {
	Token     string    `json:"token"`
	Kind      Kind      `json:"kind"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	SavedAt   time.Time `json:"saved_at"`
	// Where it was stored, for `whoami` and for `logout` to remove it. Empty for env.
	Path string `json:"-"`
}

// Expired reports whether a stored expiry has passed. Unknown expiry is not expired.
func (c *Credential) Expired(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now)
}

// ExpiresWithin reports whether the credential will expire within d.
func (c *Credential) ExpiresWithin(now time.Time, d time.Duration) bool {
	return !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(now.Add(d))
}

// Store reads and writes credentials under the config directory.
type Store struct {
	Dir string
	Env func(string) string
}

// ErrNotLoggedIn is returned when no credential is available for a host.
// Callers wrap it with NotLoggedInError so the message names the host.
var ErrNotLoggedIn = errors.New("not signed in")

// NotLoggedInError explains WHICH API the CLI looked for a credential for, and
// which ones it does have. Credentials are keyed by host, so a person who just
// signed in to localhost and then ran a command against the production default
// is "not signed in" for a reason the bare phrase hides.
type NotLoggedInError struct {
	APIURL string
	// Other API hosts that do have a stored credential.
	OtherHosts []string
}

func (e *NotLoggedInError) Error() string {
	msg := fmt.Sprintf("not signed in to %s — run `cloud login`, or set CLOUD_TOKEN", e.APIURL)
	if len(e.OtherHosts) > 0 {
		msg += fmt.Sprintf(" (you are signed in to %s; use --api-url or `cloud context use` to target it)", strings.Join(e.OtherHosts, ", "))
	}
	return msg
}

func (e *NotLoggedInError) Is(target error) bool { return target == ErrNotLoggedIn }
func (e *NotLoggedInError) Unwrap() error        { return ErrNotLoggedIn }

// hostKey turns an API URL into a filename-safe key: host and port only.
func hostKey(apiURL string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid API URL %q", apiURL)
	}
	return strings.ReplaceAll(strings.ToLower(u.Host), ":", "_"), nil
}

func (s *Store) path(apiURL string) (string, error) {
	key, err := hostKey(apiURL)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, "tokens", key+".json"), nil
}

// Load returns the credential for apiURL: CLOUD_TOKEN if set, else the stored one.
func (s *Store) Load(apiURL string) (*Credential, error) {
	if s.Env != nil {
		if t := strings.TrimSpace(s.Env("CLOUD_TOKEN")); t != "" {
			return &Credential{Token: t, Kind: KindEnv, SavedAt: time.Now()}, nil
		}
	}
	p, err := s.path(apiURL)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &NotLoggedInError{APIURL: apiURL, OtherHosts: s.Hosts()}
	}
	if err != nil {
		return nil, fmt.Errorf("reading credential: %w", err)
	}
	var c Credential
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("credential file %s is corrupt; run `cloud logout` then `cloud login`", p)
	}
	if c.Token == "" {
		return nil, &NotLoggedInError{APIURL: apiURL, OtherHosts: s.Hosts()}
	}
	c.Path = p
	return &c, nil
}

// Save writes the credential with owner-only permissions on file and directory.
func (s *Store) Save(apiURL string, c *Credential) error {
	p, err := s.path(apiURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	c.SavedAt = time.Now()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file and rename so a crash never leaves a half-written
	// credential — and never a world-readable one.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	c.Path = p
	return nil
}

// Clear removes the stored credential for apiURL. Missing is not an error.
func (s *Store) Clear(apiURL string) error {
	p, err := s.path(apiURL)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// KindOfToken guesses the kind from the token's shape, for tokens supplied via
// --token-stdin where the user did not say.
func KindOfToken(token string) Kind {
	if strings.HasPrefix(token, "sc_") {
		return KindAPIToken
	}
	return KindSession
}

// Hosts lists the API hosts that have a stored credential, as host[:port].
func (s *Store) Hosts() []string {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "tokens"))
	if err != nil {
		return nil
	}
	var hosts []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		hosts = append(hosts, strings.ReplaceAll(strings.TrimSuffix(name, ".json"), "_", ":"))
	}
	return hosts
}
