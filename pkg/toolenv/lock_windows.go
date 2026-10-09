//go:build windows

package toolenv

import (
	"context"
	"fmt"
)

func lock(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("tool preparation requires a Unix worker")
}
