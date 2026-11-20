// Package pnpmtest builds local test archives. It does not replace the package
// manager, lifecycle execution or shared worker preparation under test.
package pnpmtest

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Archive(t *testing.T, dir, name, version, script string, dependencies, optional map[string]string) string {
	t.Helper()
	manifest := map[string]any{"name": name, "version": version, "bin": map[string]string{name: "cli.js"}}
	if script != "" {
		manifest["scripts"] = map[string]string{"postinstall": script}
	}
	if len(dependencies) > 0 {
		manifest["dependencies"] = dependencies
	}
	if len(optional) > 0 {
		manifest["optionalDependencies"] = optional
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"-"+version+".tgz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tr := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
		mode int64
	}{
		{"package/package.json", data, 0644},
		{"package/cli.js", []byte("#!/usr/bin/env node\nconsole.log('" + name + "@" + version + "')\n"), 0755},
	} {
		if err = tr.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.data)), Mode: entry.mode}); err != nil {
			t.Fatal(err)
		}
		if _, err = tr.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err = tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return "file:" + path
}

const BuildScript = `node -e "require('fs').writeFileSync('built', 'yes')"`

// Serve exposes a fixture as a portable remote reference for repository config.
func Serve(t *testing.T, spec string) string {
	t.Helper()
	path := strings.TrimPrefix(spec, "file:")
	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(path))))
	t.Cleanup(server.Close)
	return server.URL + "/" + filepath.Base(path)
}
