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

type Output struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
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
	Read(context.Context, host.Host, string) (Output, error)
	Close(context.Context, host.Host, string) error
}
