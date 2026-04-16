package common

import (
	"errors"
	"fmt"
)

var ErrNotFastForward = errors.New("not a fast-forward update")

type NotFastForwardError struct {
	TargetBranch string
	LocalHead    string
	UpstreamHead string
}

func (e NotFastForwardError) Error() string {
	return fmt.Sprintf("branch %s is not a fast-forward candidate (local=%s, upstream=%s)", e.TargetBranch, e.LocalHead, e.UpstreamHead)
}

func (e NotFastForwardError) Is(target error) bool {
	if target == nil {
		return false
	}
	if target == ErrNotFastForward {
		return true
	}
	_, ok := target.(NotFastForwardError)
	if ok {
		return true
	}
	_, ok = target.(*NotFastForwardError)
	return ok
}
