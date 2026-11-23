package resourceretry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourceRetryCreationFailure(t *testing.T) {
	spawn := &os.PathError{Op: "fork/exec", Path: "/usr/bin/git", Err: syscall.EAGAIN}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"spawn", spawn, true},
		{"wrapped spawn", fmt.Errorf("clone workspace: %w", spawn), true},
		{"thread", os.NewSyscallError("pthread_create", syscall.EAGAIN), true},
		{"flattened upstream", errors.New("clone workspace: failed to clone repository: fork/exec /usr/bin/git: resource temporarily unavailable (output: )"), true},
		{"git child", errors.New("exit status 128 (output: fatal: cannot fork git-upload-pack: Resource temporarily unavailable)"), true},
		{"git clone child", errors.New("exit status 128 (output: Cloning into 'worktree'...\nerror: cannot fork() for git-upload-pack: Resource temporarily unavailable\n)"), true},
		{"git clone child with fatal summary", errors.New("exit status 128 (output: Cloning into 'worktree'...\nerror: cannot fork() for git-upload-pack 'repo/.git': Resource temporarily unavailable\nfatal: unable to fork\n)"), true},
		{"git fatal summary without detail", errors.New("exit status 128 (output: Cloning into 'worktree'...\nfatal: unable to fork\n)"), false},
		{"git fatal summary after shell warning", errors.New("exit status 128 (output: bash: fork: retry: Resource temporarily unavailable\nfatal: unable to fork\n)"), false},
		{"git fatal summary with another errno", errors.New("exit status 128 (output: error: cannot fork() for git-upload-pack: Input/output error\nfatal: unable to fork\n)"), false},
		{"git fatal summary after nonmatching creation detail", errors.New("exit status 128 (output: failed to create thread: Resource temporarily unavailable\nfatal: unable to fork\n)"), false},
		{"git fatal summary before fork detail", errors.New("exit status 128 (output: fatal: unable to fork\nerror: cannot fork() for git-upload-pack: Resource temporarily unavailable\n)"), false},
		{"git paired fork failure plus permanent error", errors.New("exit status 128 (output: error: cannot fork() for git-upload-pack: Resource temporarily unavailable\nfatal: unable to fork\nfatal: bad object HEAD\n)"), false},
		{"shell warning only", errors.New("bash: fork: retry: Resource temporarily unavailable"), false},
		{"shell terminal", errors.New("bash: fork: Resource temporarily unavailable"), true},
		{"shell warning then terminal", errors.New("bash: fork: retry: Resource temporarily unavailable\nbash: fork: Resource temporarily unavailable"), true},
		{"warning then permanent git failure", errors.New("exit status 128 (output: bash: fork: retry: Resource temporarily unavailable\nfatal: Remote branch missing not found in upstream origin)"), false},
		{"terminal then unrelated failure", errors.New("exit status 128 (output: fatal: cannot fork: Resource temporarily unavailable\nfatal: bad object HEAD)"), false},
		{"flattened spawn conflicting lines", errors.New("fatal: bad object HEAD\nclone workspace: fork/exec /usr/bin/git: resource temporarily unavailable (output: )"), false},
		{"flattened spawn contaminated output", errors.New("clone workspace: fork/exec /usr/bin/git: resource temporarily unavailable (output: fatal: bad revision)"), false},
		{"pthread", errors.New("fatal: pthread_create failed: EAGAIN"), true},
		{"rust thread creation", errors.New(`fatal: failed to spawn thread: Os { code: 11, kind: WouldBlock, message: "Resource temporarily unavailable" }`), true},
		{"wrong rust errno", errors.New(`failed to spawn thread: Os { code: 12, kind: WouldBlock, message: "Resource temporarily unavailable" }`), false},
		{"unattributed rust IO", errors.New(`Os { code: 11, kind: WouldBlock, message: "Resource temporarily unavailable" }`), false},
		{"bare errno", syscall.EAGAIN, false},
		{"nonblocking IO", os.NewSyscallError("read", syscall.EAGAIN), false},
		{"file IO", &os.PathError{Op: "read", Path: "file", Err: syscall.EAGAIN}, false},
		{"permission", &os.PathError{Op: "fork/exec", Path: "git", Err: syscall.EACCES}, false},
		{"unattributed", errors.New("resource temporarily unavailable"), false},
		{"wouldblock", errors.New("WouldBlock (os error 11)"), false},
		{"network", errors.New("connection refused: fork/exec git: resource temporarily unavailable"), false},
		{"auth conflict", errors.New("fork/exec git: resource temporarily unavailable (output: Authentication failed)"), false},
		{"truncated", errors.New("fork/exec git: resource temporarily unavailable [truncated]"), false},
		{"redacted", errors.New("fork/exec git: resource temporarily unavailable [REDACTED]"), false},
		{"oversized", errors.New("fork/exec git: resource temporarily unavailable" + strings.Repeat("x", 65536)), false},
		{"cancel", errors.Join(spawn, context.Canceled), false},
		{"deadline", fmt.Errorf("fork/exec git: resource temporarily unavailable: %w", context.DeadlineExceeded), false},
		{"cleanup", errors.Join(spawn, errors.New("cleanup failed")), false},
		{"nested conflicting cause", &os.PathError{Op: "fork/exec", Path: "git", Err: errors.Join(syscall.EAGAIN, errors.New("cleanup failed"))}, false},
		{"two resource causes", errors.Join(spawn, spawn), true},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, CreationFailure(tc.err)) })
	}
}

func TestResourceRetryMarkerAndRedaction(t *testing.T) {
	signal := Restart("local_spawn", "launch")
	require.True(t, Is(signal))
	require.False(t, Is(Restart("preparation_diagnostic", "launch")))
	require.False(t, Is(errors.Join(signal, errors.New("cleanup failed"))))
	require.False(t, Is(errors.Join(signal, context.Canceled)))
	wrapped := fmt.Errorf("private-token: %w", signal)
	redacted := Redact(wrapped, func(error) error { t.Fatal("clean controls must survive redaction"); return nil })
	require.True(t, Is(redacted))
	require.NotContains(t, redacted.Error(), "private-token")
	require.NotContains(t, fmt.Sprintf("%#v", redacted), "private-token")
	require.False(t, Supported(context.Background()))
	require.True(t, Supported(WithSupport(context.Background(), true)))
}
