// Package config resolves where the CLI talks to and as whom.
//
// Resolution order, highest wins:
//
//	flag  >  environment (CLOUD_*)  >  active context in config.yaml  >  built-in default
//
// The built-in default is the production API. Contexts exist so staging and
// local development can coexist on one machine; the environment overrides
// exist for CI, where writing a config file is a nuisance.
//
// Tokens are never stored in config.yaml. They live in the OS keychain where
// one exists, else in a 0600 file beside the config (see package auth).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultAPIURL is the production API. Overridable per context and by CLOUD_API_URL.
const DefaultAPIURL = "https://cloud.co.zm/api/v1"

// legacyHosts are retired hostnames. The platform 301s GET/HEAD requests from
// them, but an API client makes POSTs too, and a 301 would turn those into
// GETs and silently break every mutation. Refusing them here gives a clear
// error instead of a confusing one.
var legacyHosts = map[string]bool{
	"swiftcloud.co.zm":      true,
	"www.swiftcloud.co.zm":  true,
	"swiftcloud.africa":     true,
	"www.swiftcloud.africa": true,
}

// Context is one named target: an API, an organisation, a default region.
type Context struct {
	APIURL string `yaml:"api_url,omitempty"`
	Org    string `yaml:"org,omitempty"`
	Region string `yaml:"region,omitempty"`
}

// File is the on-disk shape of config.yaml.
type File struct {
	CurrentContext string             `yaml:"current_context,omitempty"`
	Contexts       map[string]Context `yaml:"contexts,omitempty"`
}

// Overrides carries values from flags; empty strings mean "not set".
type Overrides struct {
	Context string
	APIURL  string
	Org     string
	Region  string
}

// Resolved is what every command actually uses.
type Resolved struct {
	ContextName string
	APIURL      string
	Org         string
	Region      string
	// Source names where each value came from, for `cloud whoami` and debugging.
	Source map[string]string
}

// Env is the environment lookup, injectable for tests.
type Env func(key string) string

// Dir returns the configuration directory for this OS.
//
//	Linux/BSD: $XDG_CONFIG_HOME/cloud or ~/.config/cloud
//	macOS:     ~/.config/cloud (deliberately not ~/Library, so dotfile tooling finds it)
//	Windows:   %APPDATA%\cloud
func Dir(env Env) (string, error) {
	if v := env("CLOUD_CONFIG_DIR"); v != "" {
		return v, nil
	}
	if runtime.GOOS == "windows" {
		if v := env("APPDATA"); v != "" {
			return filepath.Join(v, "cloud"), nil
		}
	}
	if v := env("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "cloud"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "cloud"), nil
}

// Load reads config.yaml. A missing file is not an error: it is the state
// every fresh machine starts in, and the defaults must work there.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Contexts: map[string]Context{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if f.Contexts == nil {
		f.Contexts = map[string]Context{}
	}
	return &f, nil
}

// Save writes config.yaml with owner-only permissions, creating the directory.
func Save(path string, f *File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Resolve applies the precedence rules and validates the API URL.
func Resolve(f *File, env Env, o Overrides) (*Resolved, error) {
	r := &Resolved{Source: map[string]string{}}

	// Which context.
	switch {
	case o.Context != "":
		r.ContextName = o.Context
		r.Source["context"] = "flag"
	case env("CLOUD_CONTEXT") != "":
		r.ContextName = env("CLOUD_CONTEXT")
		r.Source["context"] = "env"
	case f.CurrentContext != "":
		r.ContextName = f.CurrentContext
		r.Source["context"] = "config"
	}
	var ctx Context
	if r.ContextName != "" {
		c, ok := f.Contexts[r.ContextName]
		if !ok {
			return nil, fmt.Errorf("context %q is not defined in the config file", r.ContextName)
		}
		ctx = c
	}

	pick := func(name, flag, envKey, ctxVal, def string) string {
		switch {
		case flag != "":
			r.Source[name] = "flag"
			return flag
		case env(envKey) != "":
			r.Source[name] = "env"
			return env(envKey)
		case ctxVal != "":
			r.Source[name] = "context"
			return ctxVal
		default:
			if def != "" {
				r.Source[name] = "default"
			}
			return def
		}
	}

	r.APIURL = strings.TrimRight(pick("api_url", o.APIURL, "CLOUD_API_URL", ctx.APIURL, DefaultAPIURL), "/")
	r.Org = pick("org", o.Org, "CLOUD_ORG", ctx.Org, "")
	r.Region = pick("region", o.Region, "CLOUD_REGION", ctx.Region, "")

	if err := ValidateAPIURL(r.APIURL); err != nil {
		return nil, err
	}
	return r, nil
}

// ValidateAPIURL rejects URLs the CLI must not talk to.
func ValidateAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("API URL %q is not a valid URL", raw)
	}
	host := strings.ToLower(u.Hostname())
	if legacyHosts[host] {
		return fmt.Errorf("API URL %q points at a retired hostname; use %s", raw, DefaultAPIURL)
	}
	// Plain HTTP is only for local development. Anything else would send a
	// bearer token in the clear.
	if u.Scheme != "https" && !isLoopback(host) {
		return fmt.Errorf("API URL %q must use https (plain http is allowed only for localhost)", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("API URL %q must use https", raw)
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}
