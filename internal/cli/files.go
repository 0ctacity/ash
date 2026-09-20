package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, entry.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
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
