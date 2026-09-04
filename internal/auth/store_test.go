package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestStore_RoundTrip_OwnerOnlyPermissions(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(nil)}
	api := "https://cloud.co.zm/api/v1"
	in := &Credential{Token: "abc", Kind: KindSession, ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.Save(api, in); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(in.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("credential file must be 0600, got %o", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(in.Path))
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("tokens dir must be 0700, got %o", dst.Mode().Perm())
	}
	out, err := s.Load(api)
	if err != nil {
		t.Fatal(err)
	}
	if out.Token != "abc" || out.Kind != KindSession {
		t.Errorf("round trip lost data: %+v", out)
	}
	if _, err := os.Stat(in.Path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Error("temp file left behind after save")
	}
}

func TestStore_NotLoggedIn(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(nil)}
	_, err := s.Load("https://cloud.co.zm/api/v1")
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("want ErrNotLoggedIn, got %v", err)
	}
	if !strings.Contains(err.Error(), "cloud login") || !strings.Contains(err.Error(), "CLOUD_TOKEN") {
		t.Errorf("error must name both ways in: %v", err)
	}
}

// Staging and production logins must not overwrite each other.
func TestStore_KeyedByHost(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(nil)}
	_ = s.Save("https://cloud.co.zm/api/v1", &Credential{Token: "prod", Kind: KindSession})
	_ = s.Save("https://staging.cloud.co.zm/api/v1", &Credential{Token: "stg", Kind: KindSession})
	_ = s.Save("http://localhost:5173/api/v1", &Credential{Token: "dev", Kind: KindSession})
	for url, want := range map[string]string{
		"https://cloud.co.zm/api/v1":         "prod",
		"https://staging.cloud.co.zm/api/v1": "stg",
		"http://localhost:5173/api/v1":       "dev",
	} {
		c, err := s.Load(url)
		if err != nil || c.Token != want {
			t.Errorf("%s: got %v %v, want %s", url, c, err, want)
		}
	}
}

// CI: CLOUD_TOKEN wins and nothing on disk is consulted.
func TestStore_EnvOverrides(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(map[string]string{"CLOUD_TOKEN": " sc_fromenv "})}
	_ = s.Save("https://cloud.co.zm/api/v1", &Credential{Token: "stored", Kind: KindSession})
	c, err := s.Load("https://cloud.co.zm/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "sc_fromenv" || c.Kind != KindEnv {
		t.Errorf("env token must win and be trimmed: %+v", c)
	}
}

func TestStore_ClearIsIdempotent(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(nil)}
	api := "https://cloud.co.zm/api/v1"
	_ = s.Save(api, &Credential{Token: "x", Kind: KindSession})
	if err := s.Clear(api); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(api); err != nil {
		t.Errorf("clearing twice must not fail: %v", err)
	}
	if _, err := s.Load(api); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("after clear, want ErrNotLoggedIn, got %v", err)
	}
}

func TestStore_CorruptFileGivesActionableError(t *testing.T) {
	s := &Store{Dir: t.TempDir(), Env: env(nil)}
	api := "https://cloud.co.zm/api/v1"
	p, _ := s.path(api)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("{nope"), 0o600)
	_, err := s.Load(api)
	if err == nil || !strings.Contains(err.Error(), "cloud logout") {
		t.Errorf("want an error telling the user how to recover, got %v", err)
	}
}

func TestCredential_Expiry(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	c := &Credential{ExpiresAt: now.Add(2 * time.Hour)}
	if c.Expired(now) {
		t.Error("not yet expired")
	}
	if !c.ExpiresWithin(now, 24*time.Hour) {
		t.Error("expires within a day")
	}
	if c.ExpiresWithin(now, time.Hour) {
		t.Error("does not expire within an hour")
	}
	if (&Credential{}).Expired(now) {
		t.Error("unknown expiry must not read as expired")
	}
}

func TestKindOfToken(t *testing.T) {
	if KindOfToken("sc_abc") != KindAPIToken || KindOfToken("session.token") != KindSession {
		t.Error("kind detection by prefix is wrong")
	}
}
