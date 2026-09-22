//go:build !windows

package doctor

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCacheCheckCommandShellStates executes the real probe command through
// /bin/sh against controlled $HOME layouts, so every exit code is pinned to
// actual shell semantics rather than a mock. The probe must also leave the
// tree exactly as it found it.
func TestCacheCheckCommandShellStates(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based branches are meaningless for root: -w is always true")
	}

	cases := map[string]struct {
		setup    func(t *testing.T, home string)
		wantExit int
	}{
		"existing writable cache": {
			setup: func(t *testing.T, home string) {
				if err := os.MkdirAll(filepath.Join(home, ".cache", "ash"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantExit: 0,
		},
		"existing non-writable cache": {
			setup: func(t *testing.T, home string) {
				dir := filepath.Join(home, ".cache", "ash")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			},
			wantExit: 1,
		},
		"ash exists but is not a directory": {
			setup: func(t *testing.T, home string) {
				if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".cache", "ash"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantExit: 2,
		},
		"missing cache with writable parent": {
			setup: func(t *testing.T, home string) {
				if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantExit: 3,
		},
		"nothing exists with writable home": {
			setup:    func(t *testing.T, home string) {},
			wantExit: 3,
		},
		"cache exists but is not a directory": {
			setup: func(t *testing.T, home string) {
				if err := os.WriteFile(filepath.Join(home, ".cache"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantExit: 4,
		},
		"missing cache with non-writable parent": {
			setup: func(t *testing.T, home string) {
				dir := filepath.Join(home, ".cache")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			},
			wantExit: 5,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			tc.setup(t, home)
			before := snapshot(t, home)

			cmd := exec.Command("/bin/sh", "-c", cacheCheckCommand)
			env := make([]string, 0, len(os.Environ()))
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "HOME=") {
					env = append(env, e)
				}
			}
			cmd.Env = append(env, "HOME="+home)
			err := cmd.Run()
			exit := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run probe: %v", err)
				}
				exit = exitErr.ExitCode()
			}
			if exit != tc.wantExit {
				t.Fatalf("probe exit = %d, want %d", exit, tc.wantExit)
			}

			// The probe is diagnostic-only: the tree must be unchanged.
			after := snapshot(t, home)
			if len(after) != len(before) {
				t.Fatalf("probe mutated the tree: before %d entries, after %d", len(before), len(after))
			}
			for p := range before {
				if !after[p] {
					t.Fatalf("probe removed %s", p)
				}
			}
		})
	}
}

// snapshot records every path below root, including root itself.
func snapshot(t *testing.T, root string) map[string]bool {
	t.Helper()
	paths := make(map[string]bool)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths[p] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}
