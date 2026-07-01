package runjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
)

type Options struct {
	JobID          string
	JobDBURI       string
	TenantID       string
	SWFURL         string
	WorkerID       string
	OnNotReady     string
	InputMode      string
	WaitTimeout    time.Duration
	PollInterval   time.Duration
	LeaseDuration  time.Duration
	AwaitThreshold time.Duration
	CI             bool
	WorkingDir     string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
}

func (o *Options) Complete(ctx context.Context) error {
	if o.WaitTimeout == 0 {
		o.WaitTimeout = 15 * time.Minute
	}
	if o.PollInterval == 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.LeaseDuration == 0 {
		o.LeaseDuration = 60 * time.Second
	}
	if o.AwaitThreshold == 0 {
		o.AwaitThreshold = 30 * time.Second
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if strings.TrimSpace(o.WorkingDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			o.WorkingDir = cwd
		}
	}
	if strings.TrimSpace(o.WorkingDir) != "" {
		if absPath, err := filepath.Abs(o.WorkingDir); err == nil {
			o.WorkingDir = absPath
		}
	}
	if strings.TrimSpace(o.OnNotReady) == "" {
		o.OnNotReady = "wait"
	}
	if strings.TrimSpace(o.InputMode) == "" {
		if o.CI || !isTerminalReader(o.Stdin) {
			o.InputMode = "ops"
		} else {
			o.InputMode = "prompt"
		}
	}
	target, err := defaults.ResolveJobDBTarget(ctx, o.WorkingDir, o.JobDBURI)
	if err != nil {
		return err
	}
	o.JobDBURI = target.URI
	o.SWFURL = target.RuntimeURL
	o.TenantID = target.TenantID
	return nil
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.JobID) == "" {
		return fmt.Errorf("--job-id is required")
	}
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	switch o.InputMode {
	case "prompt", "ops", "fail":
	default:
		return fmt.Errorf("unsupported --input-mode %q", o.InputMode)
	}
	switch o.OnNotReady {
	case "wait", "fail", "fail-on-lease", "fail-on-pending-jobs", "fail-on-future", "fail-on-missing-capability":
	default:
		return fmt.Errorf("unsupported --on-not-ready %q", o.OnNotReady)
	}
	if o.WaitTimeout < 0 {
		return fmt.Errorf("--wait-timeout must be >= 0")
	}
	if o.PollInterval <= 0 {
		return fmt.Errorf("--poll-interval must be > 0")
	}
	if o.LeaseDuration <= 0 {
		return fmt.Errorf("--lease-duration must be > 0")
	}
	return nil
}

func isTerminalReader(r io.Reader) bool {
	return isTerminalFile(r)
}

func isTerminalWriter(w io.Writer) bool {
	return isTerminalFile(w)
}

func isTerminalFile(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }
func (e exitError) ExitCode() int { return e.code }
