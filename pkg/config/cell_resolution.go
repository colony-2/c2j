package config

import (
	"context"

	"github.com/colony-2/c2j/pkg/cellref"
)

// CellResolution captures effective settings; workers never execute config commands.
func (c *ProjectConfig) CellResolution(ctx context.Context) (*cellref.Context, error) {
	out := &cellref.Context{BaseDir: c.RootDir()}
	var err error
	if out.Pattern, err = c.Pattern(ctx); err != nil {
		return nil, err
	}
	if out.SelfRepo, err = c.SelfRepo(ctx); err != nil {
		return nil, err
	}
	if out.SelfRef, err = c.SelfRef(ctx); err != nil {
		return nil, err
	}
	if out.RootRepo, err = c.RootRepo(ctx); err != nil {
		return nil, err
	}
	if out.RootRef, err = c.RootRef(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
