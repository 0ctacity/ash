package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ash/internal/host"
	"ash/internal/policy"
	"ash/internal/service"
	"ash/internal/transport"
)

type downloadTransport struct {
	fileTransport
}

// TestDownloadRefusesSymlinkedDestinationComponent pins the fix for writing
// outside the local root when an existing directory component is a symlink.
func TestDownloadRefusesSymlinkedDestinationComponent(t *testing.T) {
	local := t.TempDir()
	outside := t.TempDir()
	escape := filepath.Join(local, "out")
	if err := os.Symlink(outside, escape); err != nil {
		if strings.Contains(err.Error(), "symlink") && strings.Contains(err.Error(), "privilege") {
			t.Skip("symlink creation not permitted")
		}
		t.Fatal(err)
	}
	f := &fileTransport{readTree: []transport.TreeEntry{{Path: "out/secret.txt", Data: []byte("pwn")}}}
	s := service.New(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Read: true, Write: true}}}), f)
	err := runDownload(context.Background(), []string{"h", "/remote", local}, s)
	if err == nil {
		t.Fatal("download through a symlinked destination directory must fail")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nothing may have been written through the symlink.
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("file escaped the download root: %v", err)
	}
}

// A pre-existing symlink at the exact file destination is also refused.
func TestDownloadRefusesSymlinkedFileTarget(t *testing.T) {
	local := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim.txt"), filepath.Join(local, "sub", "x.txt")); err != nil {
		t.Fatal(err)
	}
	f := &fileTransport{readTree: []transport.TreeEntry{{Path: "sub/x.txt", Data: []byte("pwn")}}}
	s := service.New(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Read: true, Write: true}}}), f)
	if err := runDownload(context.Background(), []string{"h", "/remote", local}, s); err == nil {
		t.Fatal("download over a symlinked file target must fail")
	}
	data, err := os.ReadFile(filepath.Join(outside, "victim.txt"))
	if err != nil || string(data) != "original" {
		t.Fatalf("symlink target was overwritten: %q %v", data, err)
	}
}

// The benign path keeps working: normal trees download unchanged.
func TestDownloadNormalTreeStillWorks(t *testing.T) {
	local := t.TempDir()
	f := &fileTransport{readTree: []transport.TreeEntry{
		{Path: "sub", IsDir: true},
		{Path: "sub/x.txt", Data: []byte("hi")},
		{Path: "top.txt", Data: []byte("top")},
	}}
	s := service.New(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Read: true, Write: true}}}), f)
	if err := runDownload(context.Background(), []string{"h", "/remote", local}, s); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(local, "sub", "x.txt")); err != nil || string(data) != "hi" {
		t.Fatalf("nested file: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(local, "top.txt")); err != nil || string(data) != "top" {
		t.Fatalf("top file: %q %v", data, err)
	}
}
