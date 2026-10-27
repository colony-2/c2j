package gitprofile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/colony-2/c2j/pkg/secrets"
)

const StderrArtifactName = "_c2j/git-stderr.txt"

const stderrLimit = 1 << 20
const stderrOmitted = "[Git stderr omitted: diagnostic size limit exceeded]\n"

// StderrLog belongs to one task, including its workspace and source preparation.
// Whole command records are retained so truncation cannot expose partial secrets.
type StderrLog struct {
	mu       sync.Mutex
	data     bytes.Buffer
	omitted  bool
	redactor *secrets.Redactor
}

type stderrLogKey struct{}

func WithStderrLog(ctx context.Context) (context.Context, *StderrLog) {
	log := &StderrLog{redactor: secrets.RedactorFromContext(ctx)}
	return context.WithValue(ctx, stderrLogKey{}, log), log
}

// Bytes redacts at publication time, after all credentials have been registered.
func (l *StderrLog) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	text := l.data.String()
	if l.omitted {
		text += stderrOmitted
	}
	text = l.redactor.Redact(text)
	if len(text) > stderrLimit+len(stderrOmitted) {
		// Replacement markers can expand short secret values considerably.
		return []byte(stderrOmitted)
	}
	return []byte(text)
}

func (l *StderrLog) record(cmd *exec.Cmd, stderr *stderrBuffer, err error) {
	if stderr.Len() == 0 && !stderr.omitted {
		return
	}
	operation := "git"
	if len(cmd.Args) > 1 {
		operation += " " + cmd.Args[1]
	}
	status := "completed"
	if err != nil {
		status = "failed"
	}
	record := fmt.Sprintf("--- %s (%s) ---\n%s\n", operation, status, stderr.String())
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.omitted || len(record) > stderrLimit-l.data.Len() {
		l.omitted = true
		return
	}
	l.data.WriteString(record)
}

type stderrBuffer struct {
	data    bytes.Buffer
	omitted bool
}

func (b *stderrBuffer) Len() int { return b.data.Len() }

func (b *stderrBuffer) Write(p []byte) (int, error) {
	if b.omitted {
		return len(p), nil
	}
	if len(p) > stderrLimit-b.Len() {
		b.data.Reset()
		b.omitted = true
		return len(p), nil
	}
	return b.data.Write(p)
}

func (b *stderrBuffer) String() string {
	if b.omitted {
		return stderrOmitted
	}
	return b.data.String()
}

// Run preserves configured output writers while recording stderr in the task log.
func Run(ctx context.Context, cmd *exec.Cmd) error {
	log, _ := ctx.Value(stderrLogKey{}).(*StderrLog)
	if log == nil {
		return cmd.Run()
	}
	var stderr stderrBuffer
	original := cmd.Stderr
	cmd.Stderr = &stderr
	if original != nil {
		cmd.Stderr = io.MultiWriter(original, &stderr)
	}
	err := cmd.Run()
	cmd.Stderr = original
	log.record(cmd, &stderr, err)
	// Match exec.Cmd.Output's useful ExitError.Stderr when we capture it.
	var exit *exec.ExitError
	if original == nil && errors.As(err, &exit) {
		exit.Stderr = []byte(stderr.String())
	}
	return err
}

// Output keeps stdout separate from stderr, including for machine-readable Git output.
func Output(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if _, ok := ctx.Value(stderrLogKey{}).(*StderrLog); !ok {
		return cmd.Output()
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := Run(ctx, cmd)
	return stdout.Bytes(), err
}

type combinedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *combinedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

// CombinedOutput preserves callers' combined output while logging only stderr.
func CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if _, ok := ctx.Value(stderrLogKey{}).(*StderrLog); !ok {
		return cmd.CombinedOutput()
	}
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if cmd.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var output combinedBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := Run(ctx, cmd)
	return output.data.Bytes(), err
}
