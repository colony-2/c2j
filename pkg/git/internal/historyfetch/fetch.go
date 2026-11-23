package historyfetch

import (
	"context"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/git/common"
)

// Target keeps the initial depth-one fetch for shallow callers only. The caller
// still needs to verify the commit and prove its relationship to the base.
func Target(ctx context.Context, repo string, options, refspec []string) error {
	state, err := common.ExecuteGitCommand(ctx, repo, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return fmt.Errorf("determine repository shallow state: %w", err)
	}
	args := append([]string{"fetch"}, options...)
	switch strings.TrimSpace(string(state)) {
	case "true":
		args = append(args, "--depth=1")
	case "false":
	default:
		return fmt.Errorf("unexpected repository shallow state: %q", state)
	}
	_, err = common.ExecuteGitCommand(ctx, repo, append(args, refspec...)...)
	return err
}
