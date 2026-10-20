package common

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const DefaultGitAuthor = "colony2.com <col2bot@colony2.com>"

// GitAuthorForCell uses the same bot fallback for every cell.
func GitAuthorForCell(string) string { return DefaultGitAuthor }

// EnsureGitIdentity fills missing identity fields in this repository only.
// Git's explicit author/committer environment variables retain their precedence.
func EnsureGitIdentity(ctx context.Context, repoPath string) error {
	for _, field := range []struct{ key, value string }{{"user.name", "colony2.com"}, {"user.email", "col2bot@colony2.com"}} {
		out, err := ExecuteGitCommand(ctx, repoPath, "config", "--get", field.key)
		if err != nil {
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return fmt.Errorf("read Git identity: %w", err)
			}
		}
		if strings.TrimSpace(string(out)) == "" {
			if _, err := ExecuteGitCommand(ctx, repoPath, "config", "--local", field.key, field.value); err != nil {
				return fmt.Errorf("set Git identity fallback: %w", err)
			}
		}
	}
	return nil
}
