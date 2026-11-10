package s3

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

/*
Object keys are chosen by whoever can write to the bucket, and a download
turns them into local file names. A key is therefore untrusted input to the
filesystem: "site/../../.ssh/authorized_keys" joined onto a target directory
lands outside it, and on Windows so does "site/..\..\x", because there the
backslash is a separator too.

SafeLocalPath is the one gate every download path goes through. It works on
the key text alone — no filesystem access — so a plan can be checked before
anything is written, and RunDownload then writes through an os.Root opened on
the target directory, so a symlink already inside that directory cannot carry
a write out of it either.
*/

// ErrUnsafeKey marks an object key that cannot be written under the target
// directory without escaping it.
var ErrUnsafeKey = errors.New("unsafe object key")

// SafeLocalPath returns the path under dir that rel names, or an error when
// rel would escape dir. rel is a key relative to the prefix being downloaded,
// with forward slashes.
//
// It refuses rel when it is empty, absolute, carries a volume name, contains
// a ".." segment (splitting on both "/" and "\", whatever the platform), or
// contains a NUL; and on Windows when it contains "\" or ":" at all — the
// first is a separator there, the second names a drive or an alternate data
// stream. Finally the joined path must still lie under dir.
func SafeLocalPath(dir, rel string) (string, error) {
	return safeLocalPath(dir, rel, runtime.GOOS == "windows")
}

// safeLocalPath is SafeLocalPath with the platform made explicit, so the
// Windows rules are tested on every platform.
func safeLocalPath(dir, rel string, windows bool) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w %q: %s", ErrUnsafeKey, rel, why)
	}
	if rel == "" {
		return bad("names no file")
	}
	if strings.ContainsRune(rel, 0) {
		return bad("contains a NUL byte")
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return bad("is an absolute path")
	}
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return bad(`contains a ".." segment`)
		}
	}
	if windows && strings.ContainsAny(rel, `\:`) {
		return bad(`contains "\" or ":", which Windows reads as a path separator or drive`)
	}
	local := filepath.FromSlash(rel)
	if filepath.IsAbs(local) || filepath.VolumeName(local) != "" {
		return bad("is an absolute path")
	}
	path := filepath.Join(dir, local)
	back, err := filepath.Rel(dir, path)
	if err != nil {
		return bad(err.Error())
	}
	if back == "." || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return bad("does not name a file under the target directory")
	}
	return path, nil
}
