// Package shell defines persistent remote terminal sessions.
package shell

import (
	"ash/internal/host"
	"context"
	"errors"
	"regexp"
)

const MaxInputSize = 64 << 10

var (
	ErrInvalidID   = errors.New("invalid shell ID")
	ErrNotFound    = errors.New("shell is not running")
	ErrUnavailable = errors.New("shell backend unavailable")
)
var idPattern = regexp.MustCompile(`^sh_[0-9a-f]{32}$`)

func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return ErrInvalidID
	}
	return nil
}

// ReadRequest requests either a full snapshot or only output added since a
// previous read identified by Cursor.
type ReadRequest struct {
	Cursor string `json:"cursor,omitempty"`
}

type Output struct {
	Content string `json:"content"`
	// Cursor encodes the consumed prefix of the current snapshot and can be
	// passed to the next ReadRequest.
	Cursor string `json:"cursor"`
	// Truncated reports that the underlying snapshot hit the transport bound.
	Truncated bool `json:"truncated"`
	// Resync reports that the cursor could not be applied, so Content is a full
	// bounded snapshot instead of a suffix.
	Resync bool `json:"resync"`
}
type Info struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Backend string `json:"backend"`
}
type Backend interface {
	Name() string
	Create(context.Context, host.Host, string, string) error
	List(context.Context, host.Host) ([]string, error)
	Send(context.Context, host.Host, string, string) error
	Read(context.Context, host.Host, string, ReadRequest) (Output, error)
	Close(context.Context, host.Host, string) error
}
