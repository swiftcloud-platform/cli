//go:build !windows

package auth

import (
	"os"
	"syscall"
)

// ownerUID returns the uid that owns fi, where the platform reports one.
func ownerUID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
