// Package logging configures selective structured logging using standard slog
// loggers. It does not change process globals, read environment variables, or
// depend on C2J's workflow runtime. Applications own the logger and its output.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	slogmulti "github.com/samber/slog-multi"
)

// Selector enables DEBUG and above for an exact top-level string attribute.
// Multiple selectors are ORed; ordinary baseline records always remain visible.
type Selector struct {
	Field string
	Value string
}

// Options configures an output handler. Defaults are INFO and text.
// Level may be a *slog.LevelVar owned and updated by the host.
type Options struct {
	Level  slog.Leveler
	Format string
	Debug  []Selector
}

// ParseLevel accepts the four supported CLI levels, case-insensitively.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: expected debug, info, warn, or error", value)
	}
}

// ParseSelector parses one field=value selection, splitting at the first '='.
func ParseSelector(value string) (Selector, error) {
	field, match, ok := strings.Cut(value, "=")
	s := Selector{Field: field, Value: match}
	if !ok {
		return s, fmt.Errorf("invalid log selection %q: expected field=value", value)
	}
	return s, s.validate()
}

func (s Selector) validate() error {
	switch s.Field {
	case "component", "purpose", "job_id", "node_path", "op_type", "event":
	default:
		return fmt.Errorf("unsupported log selection field %q: expected component, purpose, job_id, node_path, op_type, or event", s.Field)
	}
	if s.Value == "" {
		return fmt.Errorf("log selection %q requires a nonempty value", s.Field)
	}
	return nil
}

// New constructs a logger; nil output means stderr. All routes share one output
// handler, and first-match routing prevents duplicate records. Redaction, where
// required, must wrap this logger before deriving child loggers.
func New(output io.Writer, opts Options) (*slog.Logger, error) {
	if output == nil {
		output = os.Stderr
	}
	level := opts.Level
	if level == nil {
		level = slog.LevelInfo
	}
	for _, s := range opts.Debug {
		if err := s.validate(); err != nil {
			return nil, err
		}
	}
	// The sink must accept selected DEBUG records even at an INFO baseline.
	sinkLevel := level
	if len(opts.Debug) > 0 {
		sinkLevel = slog.LevelDebug
	}
	var sink slog.Handler
	switch opts.Format {
	case "", "text":
		sink = slog.NewTextHandler(output, &slog.HandlerOptions{Level: sinkLevel})
	case "json":
		sink = slog.NewJSONHandler(output, &slog.HandlerOptions{Level: sinkLevel})
	default:
		return nil, fmt.Errorf("invalid log format %q: expected text or json", opts.Format)
	}
	if len(opts.Debug) == 0 {
		return slog.New(sink), nil
	}
	router := slogmulti.Router()
	for _, s := range opts.Debug {
		router = router.Add(sink, slogmulti.AttrValueIs(s.Field, s.Value))
	}
	router = router.Add(sink, func(_ context.Context, r slog.Record) bool { return r.Level >= level.Level() })
	return slog.New(router.FirstMatch().Handler()), nil
}
