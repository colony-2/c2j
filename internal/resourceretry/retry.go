// Package resourceretry separates resource recognition from permission to restart.
// A signal contains only bounded runtime-owned facts, never the original cause.
package resourceretry

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"syscall"
)

const CapabilityEnv = "C2J_RESOURCE_RETRY_CONTROL"

type signal struct{ source, phase string }

func (*signal) Error() string { return "resource temporarily unavailable; restart from durable input" }

// Restart is for stopped preparation or a rejected first process start only.
func Restart(source, phase string) error {
	if (source != "local_spawn" && source != "preparation_diagnostic") ||
		(phase != "preparation" && phase != "launch") ||
		(source == "preparation_diagnostic" && phase != "preparation") {
		return errors.New("invalid resource restart evidence")
	}
	return &signal{source, phase}
}

// Is requires every cause to authorize restart. In particular, a joined cleanup,
// capture, cancellation or timeout error vetoes a previously safe signal.
func Is(err error) bool { return onlySignal(err, 0) }
func onlySignal(err error, depth int) bool {
	if err == nil || depth > 32 {
		return false
	}
	if _, ok := err.(*signal); ok {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlySignal(cause, depth+1) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlySignal(wrapped.Unwrap(), depth+1)
	}
	return false
}

// Redact keeps a clean signal independent of secret masks, and strips all
// wrappers before it crosses redaction. Conflicting causes use normal redaction.
func Redact(err error, redact func(error) error) error {
	if Is(err) {
		var s *signal
		errors.As(err, &s)
		return &signal{s.source, s.phase}
	}
	return redact(err)
}

type supportKey struct{}

func WithSupport(ctx context.Context, supported bool) context.Context {
	return context.WithValue(ctx, supportKey{}, supported)
}
func Supported(ctx context.Context) bool { value, _ := ctx.Value(supportKey{}).(bool); return value }

// Match terminal creation errors. A shell's “fork: retry:” message is only
// a warning: the shell may recover and the command may fail for another reason.
const resourceUnavailable = `[ \t]*resource temporarily unavailable`

var (
	flattenedSpawn    = regexp.MustCompile(`(?i)(?:^|:[ \t]+)fork/exec [^\r\n]+:` + resourceUnavailable + `$`)
	shellPrefix       = `(?:(?:[^:\s]*/)?(?:ba|da|z|k)?sh:[ \t]+)?`
	shellRetryWarning = regexp.MustCompile(`(?i)^` + shellPrefix + `fork:[ \t]*retry:` + resourceUnavailable + `$`)
	gitClonePreamble  = regexp.MustCompile(`^Cloning into (?:bare repository )?'[^\r\n]*'\.\.\.$`)
	gitForkFailure    = regexp.MustCompile(`(?i)^error:[ \t]+cannot fork\(\) for [^\r\n]+:` + resourceUnavailable + `$`)
	terminalCreation  = regexp.MustCompile(`(?i)^(?:(?:remote:[ \t]+)?(?:fatal|error):[ \t]+)?(?:` +
		`(?:fork/exec [^\r\n]+|(?:cannot fork|unable to fork|failed to (?:spawn|create) (?:a )?(?:thread|process))[^\r\n]*):` + resourceUnavailable + `|` +
		shellPrefix + `fork:` + resourceUnavailable + `|` +
		`pthread_create(?: failed)?:[ \t]*EAGAIN|` +
		`failed to (?:spawn|create) (?:a )?(?:thread|process):[ \t]*Os[ \t]*\{[ \t]*code:[ \t]*11,[ \t]*kind:[ \t]*WouldBlock,[ \t]*message:[ \t]*"Resource temporarily unavailable"[ \t]*\})$`)
)

// CreationFailure examines only a designated failed preparation/launcher
// diagnostic. It must never be called on prompts, successful output or tool logs.
// This recognizes the fault; the caller must separately prove safe restart.
func CreationFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if !completeDiagnostic(err.Error()) {
		return false
	}
	return creationFailure(err, err.Error(), 0)
}

func completeDiagnostic(diagnostic string) bool {
	if len(diagnostic) > 64*1024 {
		return false
	}
	lower := strings.ToLower(diagnostic)
	for _, veto := range []string{"permission denied", "authentication failed", "fatal: not a git repository", "could not resolve", "connection refused", "could not connect", "cannot allocate memory", "too many open files", "no space left", "[redacted]", "truncated", "diagnostic size limit"} {
		if strings.Contains(lower, veto) {
			return false
		}
	}
	return true
}

func creationFailure(err error, diagnostic string, depth int) bool {
	if err == nil || depth > 32 {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !creationFailure(cause, cause.Error(), depth+1) {
				return false
			}
		}
		return true
	}
	if path, ok := err.(*os.PathError); ok {
		return path.Op == "fork/exec" && onlyEAGAIN(path.Err, 0)
	}
	if call, ok := err.(*os.SyscallError); ok {
		switch call.Syscall {
		case "fork", "vfork", "clone", "clone3", "pthread_create":
			return onlyEAGAIN(call.Err, 0)
		default:
			return false
		}
	}
	if _, ok := err.(syscall.Errno); ok {
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return creationFailure(wrapped.Unwrap(), diagnostic, depth+1)
	}
	// Only a failed subprocess or a flattened error may use text evidence.
	// Other typed errors cannot borrow contextual text.
	exit, exited := err.(*exec.ExitError)
	if !exited && !reflectErrorString(err) {
		return false
	}
	return diagnosticCreationFailure(diagnostic, exit)
}

func diagnosticCreationFailure(diagnostic string, exit *exec.ExitError) bool {
	diagnostic = strings.TrimSpace(diagnostic)
	if summary, output, ok := strings.Cut(diagnostic, " (output: "); ok {
		output, complete := strings.CutSuffix(output, ")")
		if !complete {
			return false
		}
		// A flattened start failure has no subprocess output. For a real
		// process exit, only its output can establish a creation failure.
		if strings.TrimSpace(output) == "" {
			return exit == nil && !strings.ContainsAny(summary, "\r\n") && flattenedSpawn.MatchString(summary)
		}
		diagnostic = output
	} else if exit != nil {
		diagnostic = string(exit.Stderr)
		if !completeDiagnostic(diagnostic) {
			return false
		}
	} else if flattenedSpawn.MatchString(diagnostic) && !strings.ContainsAny(diagnostic, "\r\n") {
		return true
	}

	// Every nonempty output line must be accounted for. Don't treat a
	// resource warning (or even an earlier terminal-looking line) as the
	// cause of a separate, unrecognized failure in the same process output.
	terminal := false
	lines := strings.Split(strings.TrimSpace(diagnostic), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "", shellRetryWarning.MatchString(line), gitClonePreamble.MatchString(line):
		case line == "fatal: unable to fork":
			// Git adds this summary after reporting the actual fork errno.
			// It is ambiguous without that immediately preceding detail.
			if i == 0 || i != len(lines)-1 || !gitForkFailure.MatchString(strings.TrimSpace(lines[i-1])) {
				return false
			}
		case terminalCreation.MatchString(line):
			terminal = true
		default:
			return false
		}
	}
	return terminal
}

func onlyEAGAIN(err error, depth int) bool {
	if err == nil || depth > 32 {
		return false
	}
	if err == syscall.EAGAIN {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyEAGAIN(cause, depth+1) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyEAGAIN(wrapped.Unwrap(), depth+1)
	}
	return false
}

func reflectErrorString(err error) bool {
	// errors.New has no exported concrete type. Compare its type without
	// accepting arbitrary typed errors that implement temporary-I/O semantics.
	return reflect.TypeOf(err) == reflect.TypeOf(errors.New(""))
}
