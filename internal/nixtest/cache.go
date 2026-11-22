package nixtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type SubstitutionFixture struct {
	Drv      string   `json:"drv"`
	Paths    []string `json:"paths"`
	Payload  string
	FlakeURL string
	Server   *httptest.Server
}

// SignedCache configures only this disposable consumer process. Production
// signature, cache, builder and import-from-derivation policy is never changed.
func SignedCache(t *testing.T) SubstitutionFixture {
	t.Helper()
	var f SubstitutionFixture
	raw, err := os.ReadFile("/tmp/substitution/metadata.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &f))
	raw, err = os.ReadFile("/tmp/substitution/payload")
	require.NoError(t, err)
	f.Payload = strings.TrimSpace(string(raw))
	for _, path := range append(append([]string(nil), f.Paths...), f.Payload) {
		_, err := os.Stat(path)
		require.True(t, os.IsNotExist(err), "consumer must start cold: %s (%v)", path, err)
	}
	mode := os.Getenv("C2J_NIX_SUBSTITUTION_CASE")
	cache := "/tmp/substitution/cache"
	remove := func(path string) {
		t.Helper()
		hash, _, ok := strings.Cut(filepath.Base(path), "-")
		require.True(t, ok)
		require.NoError(t, os.Remove(filepath.Join(cache, hash+".narinfo")))
	}
	if mode == "missing" {
		remove(f.Paths[2])
	}
	if mode == "missing_closure" {
		remove(f.Payload)
	}
	keyFile := "public-key"
	if mode == "untrusted" {
		keyFile = "wrong-key"
	}
	pub, err := os.ReadFile(filepath.Join("/tmp/substitution", keyFile))
	require.NoError(t, err)
	// Repository package declarations require portable remote references. Serve
	// the same fixture source over loopback instead of bypassing that validation.
	source, err := os.ReadFile("/tmp/nix-substitution/flake.nix")
	require.NoError(t, err)
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "nix-substitution/flake.nix", Mode: 0644, Size: int64(len(source))}))
	_, err = tw.Write(source)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	mux := http.NewServeMux()
	mux.HandleFunc("/fixture.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive.Bytes())
	})
	mux.Handle("/", http.FileServer(http.Dir(cache)))
	f.Server = httptest.NewServer(mux)
	f.FlakeURL = f.Server.URL + "/fixture.tar.gz"
	t.Cleanup(f.Server.Close)
	substituters := f.Server.URL
	// Only positive consumers reach native activation. Nixpkgs dependencies use
	// their normal public signed cache; negative fixture cases have no fallback.
	if mode == "cached" {
		substituters += " https://cache.nixos.org"
	}
	t.Setenv("NIX_CONFIG", "experimental-features = nix-command flakes\n"+
		"sandbox = false\n"+
		"substituters = "+substituters+"\nextra-trusted-public-keys = "+strings.TrimSpace(string(pub))+"\n"+
		"require-sigs = true\nmax-jobs = 1\nbuilders = ssh://c2j-no-build.invalid\n"+
		"allow-import-from-derivation = true\nkeep-outputs = false\nkeep-derivations = false\n")
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\necho attempted > /tmp/c2j-remote-build-attempted\nexit 99\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	AssertNoBuilders(t)
	return f
}

func AssertNoBuilders(t *testing.T) {
	t.Helper()
	for _, marker := range []string{"/tmp/c2j-local-build-attempted", "/tmp/c2j-remote-build-attempted", "/tmp/c2j-ifd-builder-attempted"} {
		_, err := os.Stat(marker)
		require.True(t, os.IsNotExist(err), "builder must not execute: %s (%v)", marker, err)
	}
}
