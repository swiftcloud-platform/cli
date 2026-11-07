package version

import "testing"

func TestRelease_AcceptsOnlyCleanReleaseVersions(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3":        "v1.2.3",
		"v1.2.3":       "v1.2.3",
		"0.3.4":        "v0.3.4",
		"1.2.3-rc.1":   "v1.2.3-rc.1",
		"10.20.30":     "v10.20.30",
		"v2.0.0-beta1": "v2.0.0-beta1",
	} {
		got, ok := Release(in)
		if !ok || got != want {
			t.Errorf("Release(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"",
		"dev",
		"none",
		"5ed1293",                    // git describe --always with no tags
		"0.3.4-dirty",                // uncommitted changes
		"0.3.4-3-g5ed1293",           // three commits past a tag
		"v0.3.4-3-g5ed1293",          // the same, as the Makefile used to inject it
		"0.3.4-3-g5ed1293-dirty",     // both
		"1.2.3+build.5",              // build metadata
		"1.2",                        // not a full release number
		"v1",                         // nor this
		"latest",                     //
		"1.2.3.4",                    //
		"0.3.4-12-gabcdef0123456789", // long hash
	} {
		if got, ok := Release(in); ok {
			t.Errorf("Release(%q) = %q, true; want it refused", in, got)
		}
	}
}

// The Makefile's git describe output carries a "v", goreleaser's does not;
// both must report the same version.
func TestSet_DropsALeadingV(t *testing.T) {
	t.Cleanup(func() { Set("dev", "none", "unknown") })
	Set("v1.2.3", "abc", "today")
	if Version() != "1.2.3" {
		t.Errorf("Version() = %q, want 1.2.3", Version())
	}
	Set("1.2.3", "abc", "today")
	if Version() != "1.2.3" {
		t.Errorf("Version() = %q, want 1.2.3", Version())
	}
}
