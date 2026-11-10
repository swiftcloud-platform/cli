package termsafe

import "testing"

func TestString(t *testing.T) {
	cases := map[string]string{
		"plain text":                          "plain text",
		"tab\tkept":                           "tab\tkept",
		"\x1b[2J\x1b[Hfake prompt":            "[2J[Hfake prompt",
		"line\nbreak\rreturn":                 "linebreakreturn",
		"bell\x07 del\x7f nul\x00":            "bell del nul",
		"c1 \u009b31m csi":                    "c1 31m csi",
		"osc \x1b]0;title\x07 end":            "osc ]0;title end",
		"invalid \x9b byte":                   "invalid � byte",
		"unicode stays: Zambia — Lusaka ✓ 日本": "unicode stays: Zambia — Lusaka ✓ 日本",
	}
	for in, want := range cases {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLines_KeepsNewlinesOnly(t *testing.T) {
	if got := Lines("a\nb\r\n\x1b[1mc"); got != "a\nb\n[1mc" {
		t.Errorf("Lines = %q", got)
	}
}
