package submitjob

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSelfTargetRejectsEmptyCanonicalRepo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("default_ref:\n  value: main\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := resolveSelfTarget(context.Background(), root)
	if err == nil {
		t.Fatal("expected resolveSelfTarget to reject an empty canonical_repo")
	}
}
