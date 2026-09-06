// Package policy defines the capabilities granted to a configured host.
package policy

import "errors"

var ErrPermissionDenied = errors.New("operation not allowed")

type Policy struct {
	Exec  bool `toml:"exec" json:"exec"`
	Read  bool `toml:"read" json:"read"`
	Write bool `toml:"write" json:"write"`
}

func (p Policy) Check(operation string) error {
	allowed := false
	switch operation {
	case "exec":
		allowed = p.Exec
	case "read", "stat":
		allowed = p.Read
	case "write":
		allowed = p.Write
	}
	if !allowed {
		return ErrPermissionDenied
	}
	return nil
}
