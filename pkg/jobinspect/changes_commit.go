package jobinspect

import (
	"bytes"
	"strconv"
	"strings"
)

// Formatted log output supplies defaults and literal placeholders for malformed
// commits. Validate stored headers first, including excluded comparison bases.
func (o *changesObjects) validateCommit(hash string) error {
	if o.checkedCommits[hash] {
		return nil
	}
	raw, err := o.git("cat-file", "commit", hash)
	if err != nil {
		if ChangesDiagnostic(err).Code == "invalid_objects" {
			if e := o.checkTreeObjects([]changesTreeObject{{hash, "commit"}}); e != nil {
				return e
			}
		}
		return err
	}
	if err := validateChangesCommit(raw, len(hash)); err != nil {
		return err
	}
	if o.checkedCommits == nil {
		o.checkedCommits = map[string]bool{}
	}
	o.checkedCommits[hash] = true
	return nil
}

func validateChangesCommit(raw []byte, hashLength int) error {
	invalid := func() error { return changesErr("invalid_objects", "invalid stored commit headers") }
	headers, _, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok || bytes.IndexByte(raw, 0) >= 0 {
		return invalid()
	}
	lines := strings.Split(string(headers), "\n")
	validHash := func(value string) bool {
		return len(value) == hashLength && fullHash(value) && strings.Trim(value, "0") != ""
	}
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "tree ") || !validHash(strings.TrimPrefix(lines[0], "tree ")) {
		return invalid()
	}
	i := 1
	for i < len(lines) && strings.HasPrefix(lines[i], "parent ") {
		if !validHash(strings.TrimPrefix(lines[i], "parent ")) {
			return invalid()
		}
		i++
	}
	for _, header := range []string{"author ", "committer "} {
		if i >= len(lines) || !strings.HasPrefix(lines[i], header) || !validChangesCommitIdent(strings.TrimPrefix(lines[i], header)) {
			return invalid()
		}
		i++
	}
	// Preserve extension headers (encoding, signatures, mergetags and future
	// fields), including their space-prefixed continuation lines.
	extension := false
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, " ") && extension {
			continue
		}
		key, _, ok := strings.Cut(line, " ")
		if !ok || key == "" || key == "tree" || key == "parent" || key == "author" || key == "committer" {
			return invalid()
		}
		for _, c := range []byte(key) {
			if c <= ' ' || c >= 127 {
				return invalid()
			}
		}
		extension = true
	}
	return nil
}

func validChangesCommitIdent(value string) bool {
	name, rest, ok := strings.Cut(value, " <")
	if !ok || strings.ContainsAny(name, "<>") {
		return false
	}
	email, date, ok := strings.Cut(rest, "> ")
	if !ok || strings.ContainsAny(email, "<>") {
		return false
	}
	timestamp, zone, ok := strings.Cut(date, " ")
	if !ok || len(zone) != 5 || (zone[0] != '+' && zone[0] != '-') || timestamp == "" {
		return false
	}
	for _, c := range []byte(timestamp + zone[1:]) {
		if c < '0' || c > '9' {
			return false
		}
	}
	// Git rejects timestamps that overflow its signed timestamp representation.
	_, err := strconv.ParseUint(timestamp, 10, 63)
	return err == nil && (len(timestamp) == 1 || timestamp[0] != '0')
}
