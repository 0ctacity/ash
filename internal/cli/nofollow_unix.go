//go:build !windows

package cli

import (
	"errors"
	"os"
	"syscall"
)

// errSymlinkRefused reports that a symlink was found at the open target.
var errSymlinkRefused = errors.New("symlink at open target")

// openNoFollow opens path for writing, refusing to follow a symlink at the
// final component. The kernel performs the check atomically with the open.
func openNoFollow(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errSymlinkRefused
		}
		return nil, err
	}
	return file, nil
}
