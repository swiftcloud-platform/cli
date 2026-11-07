// Package termsafe makes server-supplied text safe to print to a terminal.
//
// Anything the platform sends back — an error detail, a status reason, a log
// line, a notice — is untrusted as far as the terminal is concerned. An ESC
// byte in it can move the cursor, rewrite earlier lines (hiding a warning, or
// faking a "Signed in as" line), set the window title, or on some emulators do
// worse. So every such string goes through String, or Lines where a multi-line
// message is intended, before it is written.
package termsafe

import (
	"strings"
	"unicode/utf8"
)

// String removes C0 and C1 control characters (and DEL) except tab. Newlines
// and carriage returns are removed too: use Lines where a line break is meant.
// Invalid UTF-8 is replaced with U+FFFD, so a stray 0x9b byte cannot act as an
// 8-bit CSI on a terminal that honours it.
func String(s string) string { return clean(s, false) }

// Lines is String but keeps '\n', for text that is meant to span lines.
func Lines(s string) string { return clean(s, true) }

func clean(s string, keepNewline bool) string {
	if isClean(s, keepNewline) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		case r == '\n' && keepNewline:
			b.WriteRune(r)
		case drop(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func drop(r rune) bool {
	if r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

func isClean(s string, keepNewline bool) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			return false
		}
		if r == '\n' && keepNewline {
			continue
		}
		if drop(r) {
			return false
		}
	}
	return true
}
