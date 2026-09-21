package ssh

import (
	"ash/internal/host"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

// sftpFixture serves an in-process SFTP server (github.com/pkg/sftp) over
// direct pipes, so AtomicWrite runs against real sftp.Client semantics -
// including the posix-rename@openssh.com round trip - without any network or
// configured host. The transport's SFTP dial is replaced by the fixture.
type sftpFixture struct {
	tr   *Transport
	h    host.Host
	root string
}

func newSFTPFixture(t *testing.T) *sftpFixture {
	t.Helper()
	dir := t.TempDir()

	tr, err := New(filepath.Join(dir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}

	cr, serverWrite := io.Pipe() // client reads what the server writes
	serverRead, cw := io.Pipe()  // server reads what the client writes
	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{serverRead, serverWrite})
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve()

	// The client never dials: sftp.NewClientPipe connects straight to the
	// in-process server, exercising genuine client-side extension handling.
	client, err := sftp.NewClientPipe(cr, cw)
	if err != nil {
		t.Fatal(err)
	}
	// Closing the pipes unblocks the client's receive loop, so the pipes
	// must close before the client or its Close would wait forever.
	t.Cleanup(func() {
		cw.Close()
		cr.Close()
		server.Close()
		client.Close()
	})

	tr.newSFTPDial = func(ctx context.Context, h host.Host) (*sftp.Client, func(), error) {
		return client, func() {}, nil
	}

	h := host.Host{Name: "fixture", Address: "127.0.0.1", Port: 0, User: "u"}
	return &sftpFixture{tr: tr, h: h, root: dir}
}

// sftpFor returns the fixture's SFTP client (the same instance the transport
// hands to the code under test) for out-of-band inspection.
func (f *sftpFixture) sftpFor(t *testing.T) *sftp.Client {
	t.Helper()
	fs, cleanup, err := f.tr.files(context.Background(), f.h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return fs
}

// Atomic write must create a missing file and replace an existing one through
// posix-rename, via the production Transport code path. The in-process server
// serves the real filesystem, so tests run inside a unique temp directory.
func TestAtomicWriteCreatesAndReplacesOverSFTP(t *testing.T) {
	f := newSFTPFixture(t)
	ctx := context.Background()
	target := filepath.Join(f.root, "atomic-create-replace.txt")

	if err := f.tr.AtomicWrite(ctx, f.h, target, []byte("first")); err != nil {
		t.Fatal(err)
	}
	assertSFTPContent(t, f, target, "first")

	if err := f.tr.AtomicWrite(ctx, f.h, target, []byte("second")); err != nil {
		t.Fatalf("replacement over existing file failed: %v", err)
	}
	assertSFTPContent(t, f, target, "second")
}

// A failed replacement must leave the previous destination intact and clean
// up the temporary file. The rename target is a directory, so the final
// posix rename fails after the temp file is fully written.
func TestAtomicWriteFailureKeepsOldDestinationAndCleansTemp(t *testing.T) {
	f := newSFTPFixture(t)
	ctx := context.Background()
	target := filepath.Join(f.root, "atomic-keep-old.txt")

	if err := f.tr.Mkdir(ctx, f.h, target); err != nil {
		t.Fatal(err)
	}
	if err := f.tr.AtomicWrite(ctx, f.h, target, []byte("replacement")); err == nil {
		t.Fatal("atomic write over a directory must fail")
	}
	// The destination directory still exists and no temp file remains.
	fs := f.sftpFor(t)
	if info, err := fs.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("destination altered by failed write: %v", err)
	}
	assertNoTempFiles(t, fs, f.root)
}

// A server without the posix-rename extension must yield a clear
// unsupported-operation error rather than a silent non-atomic fallback, and
// it must never delete the destination to work around the missing extension.
func TestAtomicWriteWithoutPosixRenameExtensionIsUnsupported(t *testing.T) {
	if !isUnsupportedOperation(&sftp.StatusError{Code: uint32(sftp.ErrSSHFxOpUnsupported)}) {
		t.Fatal("SSH_FX_OP_UNSUPPORTED status must be detected")
	}
	if isUnsupportedOperation(&sftp.StatusError{Code: uint32(sftp.ErrSSHFxFailure)}) {
		t.Fatal("SSH_FX_FAILURE must not be treated as unsupported")
	}
	if isUnsupportedOperation(errors.New("some transport error")) {
		t.Fatal("plain errors must not be treated as unsupported")
	}

	dir := t.TempDir()
	destination := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(destination, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed := 0
	fs := &legacyServer{
		destination: destination,
		removed:     &removed,
	}
	err := atomicWriteSFTP(fs, destination, []byte("new bytes"))
	if err == nil || !isUnsupportedOperation(err) || !strings.Contains(err.Error(), "posix-rename@openssh.com") {
		t.Fatalf("expected clear unsupported error, got %v", err)
	}
	if removed != 1 {
		t.Fatalf("temp file must still be cleaned up, Remove called %d times", removed)
	}
	// The old destination must be intact: no delete-then-rename workaround.
	data, readErr := os.ReadFile(destination)
	if readErr != nil || string(data) != "keep me" {
		t.Fatalf("destination altered: %q %v", data, readErr)
	}
}

// legacyServer simulates an SFTP server that predates the posix-rename
// extension: writing a real temp file works, but every rename answers
// SSH_FX_OP_UNSUPPORTED and nothing is ever unlinked server-side.
type legacyServer struct {
	destination string
	removed     *int
}

func (l *legacyServer) OpenFile(path string, f int) (sftpFile, error) {
	return os.OpenFile(path, f, 0o644)
}

func (l *legacyServer) PosixRename(oldname, newname string) error {
	return &sftp.StatusError{Code: uint32(sftp.ErrSSHFxOpUnsupported)}
}

func (l *legacyServer) Remove(path string) error {
	*l.removed++
	return os.Remove(path)
}

// Renaming over an existing file through the in-process server matches
// POSIX semantics, proving the fixture supports what AtomicWrite needs.
func TestPosixRenameReplacesExisting(t *testing.T) {
	f := newSFTPFixture(t)
	a, b := filepath.Join(f.root, "posix-a.txt"), filepath.Join(f.root, "posix-b.txt")
	if err := f.tr.Write(context.Background(), f.h, a, []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := f.tr.Write(context.Background(), f.h, b, []byte("BBBB")); err != nil {
		t.Fatal(err)
	}
	fs := f.sftpFor(t)
	if err := fs.PosixRename(a, b); err != nil {
		t.Fatalf("posix rename: %v", err)
	}
	if _, err := fs.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("source should be gone: %v", err)
	}
	assertSFTPContent(t, f, b, "A")
}

func assertSFTPContent(t *testing.T, f *sftpFixture, path, want string) {
	t.Helper()
	fs := f.sftpFor(t)
	got, err := fs.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer got.Close()
	data, err := io.ReadAll(got)
	if err != nil || string(data) != want {
		t.Fatalf("content of %s = %q err %v, want %q", path, data, err, want)
	}
}

func assertNoTempFiles(t *testing.T, fs *sftp.Client, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > 9 && e.Name()[:9] == ".ash-tmp-" {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}
