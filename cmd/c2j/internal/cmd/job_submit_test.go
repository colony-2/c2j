package cmd

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
)

func TestSubmitHelpPromotesConventions(t *testing.T) {
	cmd := newSubmitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--build", "--evolve", "--advanced-recipe", "--advanced-recipe-file"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help missing %s: %s", flag, &out)
		}
	}
	for _, name := range []string{"recipe", "recipe-file"} {
		flag := cmd.Flags().Lookup(name)
		if !flag.Hidden || flag.Deprecated == "" {
			t.Fatalf("legacy %s not hidden and deprecated", name)
		}
	}
}

func TestSubmitRejectsConflictingRecipeFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--build", "--evolve"},
		{"--build", "--advanced-recipe", "custom"},
		{"--evolve", "--advanced-recipe-file", "custom.yaml"},
		{"--advanced-recipe", "a", "--recipe", "b"},
		{"--advanced-recipe", "a", "--advanced-recipe-file", "b.yaml"},
	} {
		cmd := newSubmitCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "none of the others") {
			t.Fatalf("args %v: error = %v", args, err)
		}
	}
}

func TestSubmitConventionsWithInteractivePromptAndJSON(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	server := httptest.NewServer(remoteruntime.NewServer(toyruntime.New()))
	defer server.Close()
	for _, mode := range []string{"default", "build", "evolve"} {
		t.Run(mode, func(t *testing.T) {
			cmd := newSubmitCmd()
			var out, stderr bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetIn(strings.NewReader("implement the feature\n"))
			args := []string{"--cell", root, "--jobdb", server.URL + "/tenant", "--json"}
			want := mode
			if mode == "default" {
				want = "build"
			} else {
				args = append(args, "--"+mode)
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Recipe string `json:"recipe"`
				JobID  string `json:"job_id"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("invalid JSON stdout %q: %v", out.String(), err)
			}
			if result.Recipe != want || result.JobID == "" {
				t.Fatalf("wrong submission: %+v", result)
			}
			if stderr.String() != "Prompt: " {
				t.Fatalf("unexpected stderr %q", stderr.String())
			}
		})
	}
}
