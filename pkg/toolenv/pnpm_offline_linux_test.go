//go:build integration && linux

package toolenv

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Filter only this test subprocess and its descendants. TSYNC covers all Go
// threads; no privilege, network namespace, worker disconnect or proxy is used.
func denyPnpmTestNetwork() error {
	// no_new_privs belongs to an OS thread. Keep it and TSYNC installation on
	// the same thread even if the goroutine would otherwise migrate.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SOCKET), Jt: 1},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_CONNECT), Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EACCES)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set network filter no_new_privs: %w", err)
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	result, _, err := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filter)
	if err != 0 {
		return fmt.Errorf("install network seccomp TSYNC filter: %w", err)
	}
	if result != 0 {
		return unix.EBUSY
	}
	return nil
}

func TestPnpmNativeOffline(t *testing.T) {
	if file := os.Getenv("C2J_PNPM_OFFLINE_ENV"); file != "" {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		var env Environment
		require.NoError(t, json.Unmarshal(raw, &env))
		address := os.Getenv("C2J_PNPM_NETWORK_CONTROL")
		control, err := net.DialTimeout("tcp", address, 10*time.Second)
		require.NoError(t, err, "numeric connectivity control before isolation")
		require.NoError(t, control.Close())
		require.NoError(t, denyPnpmTestNetwork(), "OS-enforced network denial is mandatory")
		denied, err := net.DialTimeout("tcp", address, time.Second)
		if denied != nil {
			denied.Close()
		}
		require.Error(t, err, "network must be unavailable after filtering")
		require.Contains(t, err.Error(), "permission denied")
		require.True(t, env.Ready())
		m := Manager{Root: filepath.Dir(filepath.Dir(env.Path))}
		again, diag, err := m.Prepare(context.Background(), []Scope{{ID: "native", Packages: []string{"pnpm:pnpm@12.10.1"}}})
		require.NoError(t, err)
		require.Equal(t, env, again)
		require.Equal(t, "reused", diag.Tools[0].Outcome)
		neutral := t.TempDir()
		for _, command := range [][]string{
			{"pnpm", "--version"}, {"pn", "--version"},
			{"pnpm", "--package=pnpm@12.10.1", "dlx", "pnpm", "--version"},
			{"pnpm", "--package", "pnpm@12.10.1", "dlx", "pnpm", "--version"},
			{"sh", "-c", "pnpm --version"},
		} {
			// Use the real process binding, with no test command substitution.
			out, stderr, err := executePnpmAt(env, neutral, command)
			require.NoError(t, err, stderr)
			require.Equal(t, "12.10.1\n", out)
		}
		for _, alias := range []string{"pnpx", "pnx"} {
			out, stderr, err := executePnpmAt(env, neutral, []string{alias, "--help"})
			require.NoError(t, err, stderr)
			require.NotEmpty(t, out)
		}
		_, err = os.Stat(os.Getenv("C2J_PNPM_TRAP_LOG"))
		require.True(t, os.IsNotExist(err), "no bootstrap, Node placeholder or ambient pnpm may execute")
		t.Log("native 12.10.1 ordinary/qualified/alias/subprocess execution passed with numeric connectivity denied by seccomp; warm bootstrap traps untouched")
		return
	}
	if os.Getenv("C2J_TEST_REAL_PNPM") != "1" {
		t.Skip("set C2J_TEST_REAL_PNPM=1 for native acceptance")
	}
	if _, err := pnpmNativePlatform(runtime.GOOS, runtime.GOARCH); err != nil {
		if os.Getenv("C2J_REQUIRE_NATIVE_PNPM") == "1" {
			require.NoError(t, err)
		}
		t.Skip(err.Error())
	}
	m := Manager{Root: t.TempDir()}
	env, diag, err := m.Prepare(context.Background(), []Scope{{ID: "native", Packages: []string{"pnpm:pnpm@12.10.1"}}})
	require.NoError(t, err)
	require.True(t, env.Ready())
	t.Logf("native setup: %+v", *diag.Tools[0].Pnpm)
	t.Logf("validated payload: %+v", *env.Tools[0].Pnpm.Native)
	ips, err := net.LookupIP("registry.npmjs.org")
	require.NoError(t, err)
	address := ""
	for _, ip := range ips {
		if ip.To4() != nil {
			candidate := net.JoinHostPort(ip.String(), "443")
			if c, e := net.DialTimeout("tcp", candidate, 5*time.Second); e == nil {
				c.Close()
				address = candidate
				break
			}
		}
	}
	require.NotEmpty(t, address, "a numeric reachable control is mandatory")
	file := filepath.Join(t.TempDir(), "environment.json")
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	traps := t.TempDir()
	trapLog := filepath.Join(traps, "unexpected-call")
	for _, name := range []string{"pnpm", "node"} {
		require.NoError(t, os.WriteFile(filepath.Join(traps, name), []byte("#!/bin/sh\necho called >> \"$C2J_PNPM_TRAP_LOG\"\nexit 99\n"), 0700))
	}
	for _, name := range []string{"sh", "dirname", "sed", "readlink"} {
		path, e := exec.LookPath(name)
		require.NoError(t, e)
		require.NoError(t, os.Symlink(path, filepath.Join(traps, name)))
	}
	child := exec.Command(os.Args[0], "-test.run=^TestPnpmNativeOffline$", "-test.v")
	child.Env = append(os.Environ(), "C2J_PNPM_OFFLINE_ENV="+file, "C2J_PNPM_NETWORK_CONTROL="+address, "C2J_PNPM_TRAP_LOG="+trapLog, "PATH="+traps)
	out, err := child.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Log(strings.TrimSpace(string(out)))
}

// Exercise the OS isolation machinery independently of package preparation;
// this does not stand in for the separate payload acceptance above.
func TestPnpmNetworkIsolationControl(t *testing.T) {
	if os.Getenv("C2J_PNPM_FILTER_CHILD") == "1" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		address := listener.Addr().String()
		control, err := net.DialTimeout("tcp", address, time.Second)
		require.NoError(t, err)
		require.NoError(t, control.Close())
		require.NoError(t, denyPnpmTestNetwork())
		denied, err := net.DialTimeout("tcp", address, time.Second)
		if denied != nil {
			denied.Close()
		}
		require.Error(t, err)
		require.Contains(t, err.Error(), "permission denied")
		child := exec.Command(os.Args[0], "-test.run=^TestPnpmNetworkIsolationInherited$")
		child.Env = append(os.Environ(), "C2J_PNPM_FILTER_ADDRESS="+address)
		out, err := child.CombinedOutput()
		require.NoError(t, err, string(out))
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestPnpmNetworkIsolationControl$")
	child.Env = append(os.Environ(), "C2J_PNPM_FILTER_CHILD=1")
	out, err := child.CombinedOutput()
	require.NoError(t, err, string(out))
}
func TestPnpmNetworkIsolationInherited(t *testing.T) {
	address := os.Getenv("C2J_PNPM_FILTER_ADDRESS")
	if address == "" {
		t.Skip("executed by the isolated child control")
	}
	denied, err := net.DialTimeout("tcp", address, time.Second)
	if denied != nil {
		denied.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied")
}
