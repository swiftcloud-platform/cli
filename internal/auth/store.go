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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Kind says where a credential came from; it changes what `whoami` says and
// how expiry is described.
type Kind string

// The kinds of credential the CLI can hold: a session from `cloud login`, a
// long-lived API token from the dashboard, or one supplied as CLOUD_TOKEN.
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
	// Warn, when set, receives one-line warnings about the store's state —
	// a credential file other users can read, say. Nil discards them.
	Warn func(string)
}

func (s *Store) warn(format string, a ...any) {
	if s.Warn != nil {
		s.Warn(fmt.Sprintf(format, a...))
	}
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

// Is lets errors.Is match this against ErrNotLoggedIn, so callers can test for
// "not signed in" without knowing which host the credential was missing for.
func (e *NotLoggedInError) Is(target error) bool { return target == ErrNotLoggedIn }
func (e *NotLoggedInError) Unwrap() error        { return ErrNotLoggedIn }

// HostKey turns an API URL into a filename-safe key: host and port only.
//
// It normalises so that every spelling of one API maps to one credential
// slot: case, a trailing root dot ("cloud.co.zm."), and the scheme's default
// port (":443" for https, ":80" for http) are all dropped. Otherwise a login
// through one spelling and a logout through another would leave a token
// behind. The key is restricted to a safe alphabet, so it can never name a
// path outside the tokens directory.
func HostKey(apiURL string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid API URL %q", apiURL)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	key := host
	if strings.Contains(host, ":") {
		key = "[" + host + "]"
	}
	if port != "" {
		key = net.JoinHostPort(host, port)
	}
	key = strings.ReplaceAll(key, ":", "_")
	if host == "" || strings.Trim(key, ".") == "" {
		return "", fmt.Errorf("invalid API URL %q", apiURL)
	}
	for _, r := range key {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_[]%", r)
		if !ok {
			return "", fmt.Errorf("invalid API URL %q: unexpected character in host", apiURL)
		}
	}
	return key, nil
}

func (s *Store) tokensDir() string { return filepath.Join(s.Dir, "tokens") }

func (s *Store) path(apiURL string) (string, error) {
	key, err := HostKey(apiURL)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.tokensDir(), key+".json"), nil
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
	if err := s.secureDirs(); err != nil {
		return nil, err
	}
	// #nosec G304 -- p is the tokens directory joined with HostKey's output,
	// which is restricted to [a-z0-9.-_[]%] and so cannot leave that directory.
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &NotLoggedInError{APIURL: apiURL, OtherHosts: s.Hosts()}
	}
	if err != nil {
		return nil, fmt.Errorf("reading credential: %w", err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(p); err == nil && st.Mode().Perm() != 0o600 {
			s.warn("warning: credential file %s has mode %o; it should be 0600 — fix with `chmod 600 %s`", p, st.Mode().Perm(), p)
		}
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
//
// The file is written under a fresh random name (O_EXCL, so nothing already
// there — a planted symlink included — is reused), made 0600 before a byte of
// the token goes into it, synced, and renamed over the old one. A crash never
// leaves a half-written credential, and never a readable one.
func (s *Store) Save(apiURL string, c *Credential) (err error) {
	p, err := s.path(apiURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := s.secureDirs(); err != nil {
		return err
	}
	c.SavedAt = time.Now()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".token-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, p); err != nil {
		return err
	}
	c.Path = p
	return nil
}

// secureDirs makes the config and tokens directories owner-only. A directory
// another user can list or write lets them read a token or swap one in; one
// owned by another user is refused outright, since chmod would not help.
func (s *Store) secureDirs() error {
	for _, d := range []string{s.Dir, s.tokensDir()} {
		if err := secureDir(d); err != nil {
			return err
		}
	}
	return nil
}

func secureDir(dir string) error {
	if runtime.GOOS == "windows" {
		// No POSIX modes; the per-user profile's ACLs protect it.
		return nil
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		// A symlinked config directory is common with dotfile managers;
		// check what it points at.
		if fi, err = os.Stat(dir); err != nil {
			return err
		}
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("refusing to use %s: it is owned by another user (uid %d)", dir, uid)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		// #nosec G302 -- a directory needs the x bit to be usable; 0700 is owner-only.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is accessible to other users and could not be made private: %w", dir, err)
		}
	}
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

// ClearAll removes every stored credential and returns the hosts it removed.
func (s *Store) ClearAll() ([]string, error) {
	entries, err := os.ReadDir(s.tokensDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		isToken := strings.HasSuffix(name, ".json")
		isTemp := strings.HasPrefix(name, ".token-") && strings.HasSuffix(name, ".tmp")
		if !isToken && !isTemp {
			continue
		}
		if err := os.Remove(filepath.Join(s.tokensDir(), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		if isToken {
			removed = append(removed, hostOfKey(strings.TrimSuffix(name, ".json")))
		}
	}
	return removed, nil
}

// Stored returns the stored credential for apiURL, ignoring CLOUD_TOKEN — for
// logout, which must act on the file whatever the environment says.
func (s *Store) Stored(apiURL string) (*Credential, error) {
	fileOnly := *s
	fileOnly.Env = nil
	return fileOnly.Load(apiURL)
}

// KindOfToken guesses the kind from the token's shape, for tokens supplied via
// --token-stdin where the user did not say.
func KindOfToken(token string) Kind {
	if strings.HasPrefix(token, "sc_") {
		return KindAPIToken
	}
	return KindSession
}

func hostOfKey(key string) string { return strings.ReplaceAll(key, "_", ":") }

// Hosts lists the API hosts that have a stored credential, as host[:port].
func (s *Store) Hosts() []string {
	entries, err := os.ReadDir(s.tokensDir())
	if err != nil {
		return nil
	}
	var hosts []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		hosts = append(hosts, hostOfKey(strings.TrimSuffix(name, ".json")))
	}
	return hosts
}
