package config

import (
	"errors"
	"strings"
	"testing"
)

// "swiftcloud.co.zm." is the same host as "swiftcloud.co.zm"; a trailing root
// dot or odd case must not slip a retired host past the lookup.
func TestValidateAPIURL_NormalisesTheHostBeforeTheRetiredLookup(t *testing.T) {
	for _, u := range []string{"https://swiftcloud.co.zm./api/v1", "https://WWW.SwiftCloud.co.zm./api/v1"} {
		if err := ValidateAPIURL(u); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Errorf("%s: want the retired-host refusal, got %v", u, err)
		}
	}
}

func TestResolve_RefusesAnOrgThatCollapsesInAPath(t *testing.T) {
	f := &File{Contexts: map[string]Context{}}
	for _, org := range []string{".", "..", "a\x1bb"} {
		if _, err := Resolve(f, envOf(map[string]string{"CLOUD_ORG": org}), Overrides{}); err == nil {
			t.Errorf("CLOUD_ORG=%q must be refused", org)
		}
		if _, err := Resolve(f, envOf(nil), Overrides{Org: org}); err == nil {
			t.Errorf("--org %q must be refused", org)
		}
	}
	f.CurrentContext = "x"
	f.Contexts["x"] = Context{Org: ".."}
	if _, err := Resolve(f, envOf(nil), Overrides{}); err == nil {
		t.Error("an org of \"..\" in a context must be refused")
	}
	if _, err := Resolve(f, envOf(map[string]string{"CLOUD_ORG": "acme"}), Overrides{}); err != nil {
		t.Errorf("a normal slug must pass: %v", err)
	}
}

// logout must be able to forget a credential for a URL the CLI now refuses,
// so the resolution comes back with the error rather than instead of it.
func TestResolve_ReturnsTheResolutionWithAnAPIURLError(t *testing.T) {
	f := &File{Contexts: map[string]Context{}}
	r, err := Resolve(f, envOf(map[string]string{"CLOUD_API_URL": "https://swiftcloud.co.zm/api/v1"}), Overrides{})
	var ae *APIURLError
	if !errors.As(err, &ae) {
		t.Fatalf("want APIURLError, got %v", err)
	}
	if r == nil || r.APIURL != "https://swiftcloud.co.zm/api/v1" {
		t.Errorf("resolution must still be returned: %+v", r)
	}
}

func TestValidateSegment(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a\nb", "x\x7f"} {
		if ValidateSegment("name", bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	for _, ok := range []string{"web", "...", "a.b", "my-app", "key_abc123", "example.co.zm"} {
		if err := ValidateSegment("name", ok); err != nil {
			t.Errorf("%q must pass: %v", ok, err)
		}
	}
}
