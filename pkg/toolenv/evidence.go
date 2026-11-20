package toolenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Evidence detects accidental cache damage, including equal-size changes and
// symlink retargeting. It is not a signature against replacement of both the
// payload and the evidence by an actor able to write the cache.
type fileEvidence struct {
	Path       string `json:"path"`
	Target     string `json:"target"`
	Link       string `json:"link,omitempty"`
	Mode       uint32 `json:"mode"`
	TargetMode uint32 `json:"target_mode"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

func recordFile(path string) (fileEvidence, error) {
	e := fileEvidence{Path: path}
	s, err := os.Lstat(path)
	if err != nil {
		return e, err
	}
	e.Mode = uint32(s.Mode())
	if s.Mode()&os.ModeSymlink != 0 {
		e.Link, err = os.Readlink(path)
		if err != nil {
			return e, err
		}
	}
	e.Target, err = filepath.EvalSymlinks(path)
	if err != nil {
		return e, err
	}
	e.Target, err = filepath.Abs(e.Target)
	if err != nil {
		return e, err
	}
	f, err := os.Open(path)
	if err != nil {
		return e, err
	}
	defer f.Close()
	s, err = f.Stat()
	if err != nil {
		return e, err
	}
	if !s.Mode().IsRegular() {
		return e, fmt.Errorf("cache evidence target is not a regular file: %s", path)
	}
	e.Size = s.Size()
	e.TargetMode = uint32(s.Mode())
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return e, err
	}
	e.SHA256 = hex.EncodeToString(h.Sum(nil))
	return e, nil
}
func recordFiles(paths []string) ([]fileEvidence, error) {
	var files []fileEvidence
	for _, p := range paths {
		e, err := recordFile(p)
		if err != nil {
			return nil, err
		}
		files = append(files, e)
	}
	return files, nil
}
func filesReady(files []fileEvidence, paths []string) bool {
	if len(files) == 0 || len(files) != len(paths) {
		return false
	}
	for i, p := range paths {
		if files[i].Path != p {
			return false
		}
		e, err := recordFile(p)
		if err != nil || e != files[i] {
			return false
		}
	}
	return true
}
func writeEvidence(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.WriteFile(path+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

type bindingEvidence struct {
	Schema string         `json:"schema"`
	Tools  string         `json:"tools"`
	Files  []fileEvidence `json:"files"`
}

func bindingPaths(e Environment) []string {
	names := map[string]bool{"uvx": true, "pnpm": true, "nix": true}
	for _, t := range e.Tools {
		for _, name := range t.Executables {
			names[name] = true
		}
	}
	if e.Python != nil {
		names["python"], names["python3"] = true, true
	}
	paths := make([]string, 0, len(names))
	for name := range names {
		paths = append(paths, filepath.Join(e.Path, name))
	}
	sort.Strings(paths)
	return paths
}
func retainBindings(e Environment) (*bindingEvidence, error) {
	files, err := recordFiles(bindingPaths(e))
	if err != nil {
		return nil, err
	}
	b := &bindingEvidence{Schema: pnpmSchema, Tools: key(e.Tools), Files: files}
	if err = writeEvidence(filepath.Join(e.Path, "ready.json"), b); err != nil {
		return nil, err
	}
	return b, nil
}
func bindingsReady(e Environment) bool {
	b := e.Bindings
	if b == nil || b.Schema != pnpmSchema || b.Tools != key(e.Tools) || !filesReady(b.Files, bindingPaths(e)) {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(e.Path, "ready.json"))
	if err != nil {
		return false
	}
	var disk bindingEvidence
	return json.Unmarshal(raw, &disk) == nil && key(disk) == key(b)
}
