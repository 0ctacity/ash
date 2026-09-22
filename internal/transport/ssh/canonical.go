package ssh

import (
	"ash/internal/host"
	"ash/internal/transport"
	"context"
	"fmt"
	"path"
)

// Canonicalize resolves a remote path to a symlink-free absolute path. For a
// destination that does not exist yet it resolves the nearest existing parent
// and appends the remaining components, so callers can enforce path roots
// before a create. It never executes remote shell code.
func (t *Transport) Canonicalize(ctx context.Context, h host.Host, p string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	fs, cleanup, err := t.files(ctx, h)
	if err != nil {
		return "", err
	}
	defer cleanup()
	p, err = remotePath(fs, p)
	if err != nil {
		return "", operationError(ctx, err)
	}
	if absolute, err := fs.RealPath(p); err == nil {
		return absolute, nil
	}
	parent := path.Dir(p)
	rest := []string{path.Base(p)}
	for {
		if absolute, err := fs.RealPath(parent); err == nil {
			parts := make([]string, 0, len(rest)+1)
			parts = append(parts, absolute)
			for i := len(rest) - 1; i >= 0; i-- {
				parts = append(parts, rest[i])
			}
			return path.Join(parts...), nil
		}
		next := path.Dir(parent)
		if next == parent {
			return "", operationError(ctx, fmt.Errorf("cannot canonicalize path %q", p))
		}
		rest = append(rest, path.Base(parent))
		parent = next
	}
}
