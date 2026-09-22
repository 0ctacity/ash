package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"ash/internal/config"
	"ash/internal/host"
	"ash/internal/policy"
)

const HostAddUsage = `Usage:
  ash [--config PATH] host add NAME --address ADDRESS --user USER
      [--port PORT] [--identity PATH] [--exec] [--read] [--write]

Adds a minimal deny-by-default host entry to the configuration file.
Existing hosts or configurations are never overwritten.`

// HostAdd appends a minimal host entry to the configuration file.
func HostAdd(args []string, path string, out, errout io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errout, "ash:", err); return 1 }
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, HostAddUsage)
		return 0
	}
	name := args[0]
	h := host.Host{}
	pol := policy.Policy{}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		value := ""
		hasValue := false
		key := arg
		if k, v, ok := strings.Cut(arg, "="); ok {
			key, value, hasValue = k, v, true
		}
		needValue := func() (string, bool) {
			if hasValue {
				return value, true
			}
			i++
			if i == len(args) {
				return "", false
			}
			return args[i], true
		}
		switch key {
		case "--address":
			v, ok := needValue()
			if !ok {
				return fail(fmt.Errorf("--address requires a value"))
			}
			h.Address = v
		case "--user":
			v, ok := needValue()
			if !ok {
				return fail(fmt.Errorf("--user requires a value"))
			}
			h.User = v
		case "--port":
			v, ok := needValue()
			if !ok {
				return fail(fmt.Errorf("--port requires a value"))
			}
			port, err := strconv.Atoi(v)
			if err != nil {
				return fail(fmt.Errorf("--port requires a number"))
			}
			h.Port = port
		case "--identity":
			v, ok := needValue()
			if !ok {
				return fail(fmt.Errorf("--identity requires a value"))
			}
			h.Identity = v
		case "--exec":
			pol.Exec = true
		case "--read":
			pol.Read = true
		case "--write":
			pol.Write = true
		default:
			return fail(fmt.Errorf("unknown host add option %q", key))
		}
	}
	if h.Address == "" || h.User == "" {
		return fail(fmt.Errorf("host add requires --address and --user"))
	}
	if h.Port == 0 {
		h.Port = 22
	}
	written, err := config.AddHost(path, name, host.Host{Address: h.Address, Port: h.Port, User: h.User, Identity: h.Identity, Policy: pol})
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"host": name, "address": h.Address, "user": h.User, "port": h.Port, "config": path, "written": written}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fail(err)
	}
	fmt.Fprintf(errout, "verify it with: ash --config %s doctor %s\n", path, name)
	return 0
}
