// Package transport defines remote operation contracts shared by all adapters.
package transport

import (
	"ash/internal/host"
	"context"
	"errors"
	"time"
)

const (
	MaxReadSize        = 4 << 20
	MaxWriteSize       = 4 << 20
	MaxOutputSize      = 8 << 20
	DefaultExecTimeout = 5 * time.Minute
	DefaultFileTimeout = 30 * time.Second
)

var (
	ErrAuthentication = errors.New("authentication failed")
	ErrHostKey        = errors.New("host key verification failed")
	ErrTimeout        = errors.New("operation timed out")
	ErrTooLarge       = errors.New("file exceeds size limit")
)

type ExecRequest struct {
	Host, Command, Cwd string
	Env                map[string]string
	Timeout            time.Duration
}
type ExecResult struct {
	ExitCode                         int
	Stdout, Stderr                   string
	StdoutTruncated, StderrTruncated bool
	Duration                         time.Duration
}
type FileInfo struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modified_at"`
	IsDir   bool      `json:"is_dir"`
}
type Transport interface {
	Exec(context.Context, host.Host, ExecRequest) (ExecResult, error)
	Read(context.Context, host.Host, string) ([]byte, error)
	Write(context.Context, host.Host, string, []byte) error
	Stat(context.Context, host.Host, string) (FileInfo, error)
}
