package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"ash/internal/service"
	"ash/internal/transport"
)

func encodeOK(out io.Writer, fail func(error) int) int {
	if err := json.NewEncoder(out).Encode(map[string]bool{"ok": true}); err != nil {
		return fail(err)
	}
	return 0
}

func runWrite(ctx context.Context, args []string, s *service.Service, in io.Reader) error {
	if len(args) < 2 {
		return fmt.Errorf("write requires HOST PATH [--atomic]")
	}
	atomic := false
	for _, arg := range args[2:] {
		if arg != "--atomic" {
			return fmt.Errorf("unknown write option %q", arg)
		}
		atomic = true
	}
	data, err := readInput(ctx, in, transport.MaxWriteSize)
	if err != nil {
		return err
	}
	if atomic {
		return s.AtomicWrite(ctx, args[0], args[1], data)
	}
	return s.Write(ctx, args[0], args[1], data)
}

// runDownload maps a bounded remote tree onto a local directory. It never
// follows remote symlinks and refuses to write outside the local root.
//
// Lexical containment alone is not enough locally: MkdirAll and WriteFile
// follow existing symlinks, so a pre-existing symlinked directory inside the
// destination could redirect a tree entry outside the root. Every destination
// component is therefore checked with Lstat and any existing symlink in the
// path is refused.
func runDownload(ctx context.Context, args []string, s *service.Service) error {
	if len(args) != 3 {
		return fmt.Errorf("download requires HOST REMOTE_DIR LOCAL_DIR")
	}
	entries, err := s.ReadTree(ctx, args[0], args[1])
	if err != nil {
		return err
	}
	root := args[2]
	for _, entry := range entries {
		target := filepath.Join(root, filepath.FromSlash(entry.Path))
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("refusing to write %q outside %q", entry.Path, root)
		}
		if entry.IsDir {
			if err := mkdirAllSafe(target, root); err != nil {
				return err
			}
			continue
		}
		if err := mkdirAllSafe(filepath.Dir(target), root); err != nil {
			return err
		}
		file, err := openNewFile(target)
		if err != nil {
			return err
		}
		if _, err := file.Write(entry.Data); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

// mkdirAllSafe creates dir and any missing parents, refusing to traverse an
// existing symlink component. Components at or below root are verified; the
// root itself is the caller's responsibility.
func mkdirAllSafe(dir, root string) error {
	if dir == root || dir == string(filepath.Separator) || dir == "." {
		return ensureNotSymlink(dir)
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirAllSafe(parent, root); err != nil {
			return err
		}
	}
	if err := ensureNotSymlink(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("%q exists and is not a directory", dir)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	return os.Mkdir(dir, 0o755)
}

// ensureNotSymlink rejects a path whose final component is a symlink.
func ensureNotSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to follow symlink %q", path)
	}
	return nil
}

// openNewFile opens target for writing without following a symlink placed at
// the path (before or during the open). O_NOFOLLOW makes the kernel refuse
// symlink finals atomically where available; the Windows branch relies on the
// earlier Lstat walk plus os.OpenFile not creating symlink entries itself.
func openNewFile(target string) (*os.File, error) {
	if runtime.GOOS != "windows" {
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
		if err != nil {
			if errors.Is(err, syscall.ELOOP) {
				return nil, fmt.Errorf("refusing to write through symlink %q", target)
			}
			return nil, err
		}
		return file, nil
	}
	if err := ensureNotSymlink(target); err != nil {
		return nil, err
	}
	return os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}

// runUpload maps a local directory onto a bounded remote tree, skipping symlinks.
func runUpload(ctx context.Context, args []string, s *service.Service) error {
	if len(args) != 3 {
		return fmt.Errorf("upload requires HOST LOCAL_DIR REMOTE_DIR")
	}
	entries, err := walkLocal(args[1])
	if err != nil {
		return err
	}
	return s.WriteTree(ctx, args[0], args[2], entries)
}

func walkLocal(root string) ([]transport.TreeEntry, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("upload source must be a directory")
	}
	entries := make([]transport.TreeEntry, 0)
	total := 0
	type item struct {
		absolute string
		relative string
		depth    int
	}
	stack := []item{{absolute: root}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		children, err := os.ReadDir(current.absolute)
		if err != nil {
			return nil, err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		subdirs := make([]item, 0, len(children))
		for _, child := range children {
			if child.Type()&os.ModeSymlink != 0 {
				continue
			}
			relative := child.Name()
			if current.relative != "" {
				relative = current.relative + "/" + child.Name()
			}
			absolute := filepath.Join(current.absolute, child.Name())
			if child.IsDir() {
				if current.depth+1 > transport.MaxTreeDepth {
					return nil, fmt.Errorf("upload exceeds depth %d", transport.MaxTreeDepth)
				}
				entries = append(entries, transport.TreeEntry{Path: relative, IsDir: true})
				subdirs = append(subdirs, item{absolute: absolute, relative: relative, depth: current.depth + 1})
			} else {
				childInfo, err := child.Info()
				if err != nil {
					return nil, err
				}
				if childInfo.Size() > transport.MaxReadSize {
					return nil, fmt.Errorf("file %q exceeds %d byte limit", relative, transport.MaxReadSize)
				}
				data, err := os.ReadFile(absolute)
				if err != nil {
					return nil, err
				}
				total += len(data)
				if total > transport.MaxTreePayload {
					return nil, fmt.Errorf("upload exceeds %d byte payload", transport.MaxTreePayload)
				}
				entries = append(entries, transport.TreeEntry{Path: relative, Data: data})
			}
			if len(entries) > transport.MaxTreeEntries {
				return nil, fmt.Errorf("upload exceeds %d entries", transport.MaxTreeEntries)
			}
		}
		for i := len(subdirs) - 1; i >= 0; i-- {
			stack = append(stack, subdirs[i])
		}
	}
	return entries, nil
}
