package compiler

import (
	"errors"
	"log/slog"

	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func logReplayCacheMiss(logger *slog.Logger, message string, err error, attrs ...any) bool {
	if !isReplayCacheMiss(err) {
		return false
	}
	if logger == nil {
		logger = slog.Default()
	}
	args := append(attrs, "error", err)
	logger.Debug(message, args...)
	return true
}

func isReplayCacheMiss(err error) bool {
	var miss jobworkflow.ReplayCacheMissError
	return errors.As(err, &miss)
}
