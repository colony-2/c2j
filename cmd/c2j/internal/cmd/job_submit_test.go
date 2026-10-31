package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipejob"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func TestSubmitHelpPromotesConventions(t *testing.T) {
	cmd := newSubmitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--advanced-recipe", "--advanced-recipe-file", "--title", "--prompt-file"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help missing %s: %s", flag, &out)
		}
	}
	for _, name := range []string{"build", "evolve"} {
		if cmd.Flags().Lookup(name) != nil || strings.Contains(out.String(), "--"+name) {
			t.Fatalf("obsolete mode flag %s is still exposed", name)
		}
	}
	for _, name := range []string{"recipe", "recipe-file"} {
		flag := cmd.Flags().Lookup(name)
		if !flag.Hidden || flag.Deprecated == "" {
			t.Fatalf("legacy %s not hidden and deprecated", name)
		}
	}
}

func TestSubmitTitleFlag(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	db := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(db))
	defer server.Close()
	for _, title := range []string{"  Improve café\nUI  ", "", "  "} {
		cmd := newSubmitCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"unchanged prompt", "--title", title, "--cell", root, "--jobdb", server.URL + "/tenant", "--json"})
		require.NoError(t, cmd.Execute())
		var submitted struct {
			JobID string `json:"job_id"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &submitted))
		job, err := recipejob.GetRecipeJob(context.Background(), db, recipejob.GetRecipeJobRequest{TenantID: "tenant", JobID: submitted.JobID})
		require.NoError(t, err)
		want := title
		if strings.TrimSpace(want) == "" {
			want = ""
		}
		require.Equal(t, want, job.Title)
		require.Equal(t, "unchanged prompt", job.Prompt)
	}
	cmd := newSubmitCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"prompt", "--title", "", "--inputs-json", `{"title":"existing"}`, "--cell", root, "--jobdb", server.URL + "/tenant"})
	require.ErrorContains(t, cmd.Execute(), "title was provided both")
}

func TestSubmitRejectsConflictingRecipeFlags(t *testing.T) {
	for _, args := range [][]string{
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
	for _, mode := range []string{"default", "build", "custom"} {
		t.Run(mode, func(t *testing.T) {
			cmd := newSubmitCmd()
			var out, stderr bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetIn(strings.NewReader("implement the feature\n"))
			args := []string{"--cell", root, "--jobdb", server.URL + "/tenant", "--json"}
			want := mode
			if mode == "default" {
				want = "git+https://github.com/colony-2/recipes.git//build.yaml@main"
			} else {
				args = append(args, "--advanced-recipe", mode)
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

func TestSubmitRejectsRemovedModeFlags(t *testing.T) {
	for _, flag := range []string{"--build", "--evolve"} {
		t.Run(flag, func(t *testing.T) {
			cmd := newSubmitCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{flag})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: "+flag) {
				t.Fatalf("error = %v, want unknown flag", err)
			}
		})
	}
}

func TestSubmitPromptFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	prompt := "  Implement café support.\n\nKeep `code` and $VARIABLE literal.\n"
	require.NoError(t, os.WriteFile("prompt.md", []byte(prompt), 0600))
	require.NoError(t, os.WriteFile("inputs.yaml", []byte("extra: retained\n"), 0600))
	db := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(db))
	defer server.Close()
	cmd := newSubmitCmd()
	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("subsequent input\n")
	cmd.SetIn(stdin)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--prompt-file", "prompt.md", "--inputs-file", "inputs.yaml", "--cell", root, "--jobdb", server.URL + "/tenant", "--json"})
	require.NoError(t, cmd.Execute())
	require.Empty(t, stderr.String())
	remaining, err := io.ReadAll(stdin)
	require.NoError(t, err)
	require.Equal(t, "subsequent input\n", string(remaining))
	var submitted struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &submitted))
	job, err := recipejob.GetRecipeJob(context.Background(), db, recipejob.GetRecipeJobRequest{TenantID: "tenant", JobID: submitted.JobID})
	require.NoError(t, err)
	require.Equal(t, prompt, job.Prompt)
}

func TestSubmitPromptFileErrors(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.WriteFile("prompt.md", []byte("file prompt\n"), 0600))
	require.NoError(t, os.WriteFile("empty.md", []byte(" \n\t"), 0600))
	require.NoError(t, os.WriteFile("invalid.md", []byte{0xff}, 0600))
	require.NoError(t, os.WriteFile("inputs.yaml", []byte("prompt: from inputs\n"), 0600))
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"positional conflict", []string{"positional", "--prompt-file", "prompt.md"}, "mutually exclusive"},
		{"empty positional conflict", []string{"", "--prompt-file", "prompt.md"}, "mutually exclusive"},
		{"json conflict", []string{"--prompt-file", "prompt.md", "--inputs-json", `{"prompt":"existing"}`}, "both via --prompt-file"},
		{"file conflict", []string{"--prompt-file", "prompt.md", "--inputs-file", "inputs.yaml"}, "both via --prompt-file"},
		{"missing", []string{"--prompt-file", "missing.md"}, "read prompt file"},
		{"directory", []string{"--prompt-file", root}, "read prompt file"},
		{"empty path", []string{"--prompt-file", ""}, "path is required"},
		{"blank content", []string{"--prompt-file", "empty.md"}, "non-empty prompt"},
		{"invalid encoding", []string{"--prompt-file", "invalid.md"}, "UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newSubmitCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(append(tc.args, "--cell", root, "--jobdb", "http://127.0.0.1:1/tenant"))
			require.ErrorContains(t, cmd.Execute(), tc.want)
		})
	}
}
