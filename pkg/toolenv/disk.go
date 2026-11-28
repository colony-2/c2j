package toolenv

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type nixDiskError struct {
	error
	path string
}

func (err *nixDiskError) Unwrap() error { return err.error }

// DiskExhaustion reports local evidence from a tool preparation call, never a
// serialized diagnostic. Multi-cause errors (e.g. failed cleanup) are ambiguous.
func DiskExhaustion(err error) (path string, confirmed bool) {
	for depth := 0; err != nil && depth < 32; depth++ {
		switch cause := err.(type) {
		case *nixDiskError:
			return cause.path, true
		case *os.PathError:
			return cause.Path, cause.Err == syscall.ENOSPC
		case *os.LinkError:
			return cause.New, cause.Err == syscall.ENOSPC
		case interface{ Unwrap() error }:
			err = cause.Unwrap()
		default:
			return "", false
		}
	}
	return "", false
}

var nixStoreWriteError = regexp.MustCompile(`^(?:(?:creating|cannot create) (?:directory|file)|opening (?:lock )?file|writing to file|renaming) ['‘"]?(/nix/store/[^'’"\r\n]+?)['’"]?: No space left on device$`)
var nixFilesystemWriteError = regexp.MustCompile(`^filesystem error: cannot create directory: No space left on device \[(/nix/store/[^\[\]\r\n]+)\]$`)
var nixPathlessWriteError = regexp.MustCompile(`^(?:writing to file|write of [0-9]+ bytes): No space left on device$`)
var nixLogColor = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func nixPrefetchDiskFailure(err error, stderr []byte) (path, message string, confirmed bool) {
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		return "", "", false
	}
	stderr = bytes.TrimSpace(stderr)
	line := stderr[bytes.LastIndexByte(stderr, '\n')+1:]
	line, ok = bytes.CutPrefix(line, []byte("@nix "))
	if !ok || len(line) > 16*1024 {
		return "", "", false
	}
	var record struct {
		Action string `json:"action"`
		Level  *int   `json:"level"`
		Raw    string `json:"raw_msg"`
		Msg    string `json:"msg"`
	}
	if json.Unmarshal(line, &record) != nil || record.Action != "msg" || record.Level == nil || *record.Level != 0 {
		return "", "", false
	}
	message = nixLogColor.ReplaceAllString(record.Raw, "")
	if nixPathlessWriteError.MatchString(message) {
		return "", message, true
	}
	if match := nixStoreWriteError.FindStringSubmatch(message); match != nil && filepath.Clean(match[1]) == match[1] {
		return match[1], message, true
	}
	if record.Raw == "" {
		message = strings.TrimPrefix(nixLogColor.ReplaceAllString(record.Msg, ""), "error: ")
		if match := nixFilesystemWriteError.FindStringSubmatch(message); match != nil && filepath.Clean(match[1]) == match[1] {
			return match[1], message, true
		}
	}
	return "", "", false
}
