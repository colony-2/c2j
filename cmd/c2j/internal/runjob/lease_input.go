package runjob

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const maxLeaseInputBytes = 1024 * 1024

// readLeaseInput deliberately does not include file contents in errors. Lease
// capabilities are credentials, including when an input is malformed.
func readLeaseInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("--lease-file is required (use - to read from stdin)")
	}
	var reader io.Reader
	if path == "-" {
		if stdin == nil {
			stdin = os.Stdin
		}
		if isTerminalReader(stdin) {
			return nil, fmt.Errorf("lease input must be piped or redirected; refusing terminal input")
		}
		reader = stdin
	} else {
		// Check the opened descriptor, not a pathname that could be swapped
		// between checking its permissions and opening it. O_NONBLOCK avoids
		// hanging on a FIFO; callers can pipe a channel through stdin instead.
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, fmt.Errorf("open lease file: %w", err)
		}
		file := os.NewFile(uintptr(fd), "lease input")
		defer file.Close()
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return nil, fmt.Errorf("inspect lease file: %w", err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 || stat.Uid != uint32(os.Geteuid()) {
			return nil, fmt.Errorf("lease file must be a regular file owned by the current user with no group or other permissions (use chmod 600)")
		}
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxLeaseInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read lease input: %w", err)
	}
	if len(data) == 0 || len(data) > maxLeaseInputBytes {
		return nil, fmt.Errorf("lease input must contain between 1 byte and 1 MiB")
	}
	return data, nil
}
