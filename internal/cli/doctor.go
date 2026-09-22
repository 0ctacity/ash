package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"ash/internal/doctor"
)

// Doctor runs local and optionally remote host diagnostics.
func Doctor(ctx context.Context, args []string, d *doctor.Doctor, out, errout io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errout, "ash:", err); return 1 }
	name := ""
	asJSON := false
	for _, arg := range args {
		switch {
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			return fail(fmt.Errorf("unknown doctor option %q", arg))
		case name == "":
			name = arg
		default:
			return fail(fmt.Errorf("doctor takes at most one HOST"))
		}
	}
	report := d.Run(ctx, name)
	if asJSON {
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return fail(err)
		}
	} else {
		for _, check := range report.Checks {
			fmt.Fprintf(out, "%-4s %-12s %s\n", check.Status, check.Name, check.Message)
			if check.Hint != "" {
				fmt.Fprintf(out, "     %-12s hint: %s\n", "", check.Hint)
			}
		}
	}
	if report.Failed() {
		return 1
	}
	return 0
}
