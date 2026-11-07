//go:build windows

package auth

import "os"

// ownerUID is not meaningful on Windows, where ACLs, not a uid, decide access.
func ownerUID(os.FileInfo) (int, bool) { return 0, false }
