package runjob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestLeaseInputProtectedFileAndStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease.json")
	require.NoError(t, os.WriteFile(path, []byte("secret"), 0o600))
	data, err := readLeaseInput(path, nil)
	require.NoError(t, err)
	require.Equal(t, "secret", string(data))
	data, err = readLeaseInput("-", strings.NewReader("secret"))
	require.NoError(t, err)
	require.Equal(t, "secret", string(data))
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		require.NoError(t, os.Chmod(path, mode))
		_, err := readLeaseInput(path, nil)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}

func TestLeaseInputRejectsUnsafeFilesAndUnboundedInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lease.json")
	require.NoError(t, os.WriteFile(path, []byte("secret"), 0o600))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(path, link))
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))
	for _, path := range []string{"", link, fifo, dir} {
		_, err := readLeaseInput(path, nil)
		require.Error(t, err)
	}
	for _, content := range []string{"", strings.Repeat("x", maxLeaseInputBytes+1)} {
		_, err := readLeaseInput("-", strings.NewReader(content))
		require.Error(t, err)
	}
}
