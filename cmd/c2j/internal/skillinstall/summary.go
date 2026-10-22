package skillinstall

import (
	"fmt"
	"io"
	"strings"
)

// PrintSummary reports installed, unchanged, modified, and failed skills.
func PrintSummary(stdout io.Writer, summary Summary) {
	if len(summary.Results) == 0 {
		return
	}
	if installed := summary.Installed(); len(installed) > 0 {
		fmt.Fprintf(stdout, "installed skills: %s\n", resultNames(installed, false))
	}
	if skipped := summary.Skipped(); len(skipped) > 0 {
		fmt.Fprintf(stdout, "skipped skills: %s\n", resultNames(skipped, true))
	}
	if failed := summary.Failed(); len(failed) > 0 {
		fmt.Fprintf(stdout, "failed skills: %s\n", resultNames(failed, true))
	}
}

func resultNames(results []SkillResult, includeReason bool) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		name := result.Name
		reason := strings.TrimSpace(result.Reason)
		if result.Err != nil {
			reason = result.Err.Error()
		}
		if includeReason && reason != "" {
			name += " (" + reason + ")"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}
