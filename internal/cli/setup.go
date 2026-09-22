package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"ash/internal/setup"
)

const SetupUsage = `Usage:
  ash setup [AGENT] [--scope user|project] [--project DIR] [--print]

AGENT is one of: codex, opencode, freebuff.
--print shows the proposed configuration without writing any file.
Unsupported agents receive a manual stdio configuration example.`

// Setup registers ASH as an MCP server with a supported coding agent.
func Setup(args []string, ashPath, configPath string, out, errout io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errout, "ash:", err); return 1 }
	opts := setup.Options{AshPath: ashPath, ConfigPath: configPath}
	agent := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--print":
			opts.Print = true
		case args[i] == "--scope":
			i++
			if i == len(args) {
				return fail(fmt.Errorf("--scope requires user or project"))
			}
			opts.Scope = setup.Scope(args[i])
		case strings.HasPrefix(args[i], "--scope="):
			opts.Scope = setup.Scope(strings.TrimPrefix(args[i], "--scope="))
		case args[i] == "--project":
			i++
			if i == len(args) {
				return fail(fmt.Errorf("--project requires a directory"))
			}
			opts.ProjectDir = args[i]
		case strings.HasPrefix(args[i], "--project="):
			opts.ProjectDir = strings.TrimPrefix(args[i], "--project=")
		case strings.HasPrefix(args[i], "-"):
			return fail(fmt.Errorf("unknown setup option %q", args[i]))
		case agent == "":
			agent = args[i]
		default:
			return fail(fmt.Errorf("setup takes at most one AGENT"))
		}
	}
	if opts.Scope != "" && opts.Scope != setup.UserScope && opts.Scope != setup.ProjectScope {
		return fail(fmt.Errorf("--scope must be user or project"))
	}
	if agent == "" {
		fmt.Fprintln(out, SetupUsage)
		fmt.Fprintln(out, "\nSupported agents:")
		for _, a := range setup.Agents() {
			scopes := make([]string, 0, len(a.Scopes))
			for _, s := range a.Scopes {
				scopes = append(scopes, string(s))
			}
			fmt.Fprintf(out, "  %-10s scopes: %s\n", a.Name, strings.Join(scopes, ", "))
		}
		return 0
	}
	opts.Agent = agent
	result, err := setup.Run(opts)
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fail(err)
	}
	return 0
}
