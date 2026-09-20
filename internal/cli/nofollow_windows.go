//go:build windows

package cli

import (
	"errors"
	"os"
)

// errSymlinkRefused reports that a symlink was found at the open target.
var errSymlinkRefused = errors.New("symlink at open target")

// openNoFollow on Windows has no kernel O_NOFOLLOW equivalent exposed through
// os.OpenFile; the preceding Lstat walk in openNewFile is the guard, and the
// file is opened normally.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}
