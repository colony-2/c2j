package celledit

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/colony-2/c2j/pkg/config"
)

type Options struct {
	WorkingDir  string
	Cell        string
	Description *string
	StdoutOnly  bool
	Stdout      io.Writer
}

func Run(ctx context.Context, opts Options) error {
	if opts.WorkingDir == "" {
		var err error
		opts.WorkingDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	edit, err := config.PrepareDependentEdit(ctx, opts.WorkingDir, opts.Cell, opts.Description)
	if err != nil {
		return err
	}
	if opts.StdoutOnly {
		_, err := opts.Stdout.Write(edit.YAML)
		return err
	}
	if err := edit.Write(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(opts.Stdout, "%s %s in %s\n", edit.Status, edit.Repo, edit.Path)
	return err
}
