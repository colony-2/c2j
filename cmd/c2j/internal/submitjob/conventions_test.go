package submitjob

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmissionRecipeConventions(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts Options
		want string
	}{
		{"default", Options{}, "build"},
		{"build", Options{Build: true}, "build"},
		{"evolve", Options{Evolve: true}, "evolve"},
		{"advanced", Options{Recipe: "custom"}, "custom"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			name, embedded, cleanup, err := loadRecipeStart(tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if name != tt.want || embedded != nil {
				t.Fatalf("got %q, embedded=%v; want unresolved %q", name, embedded, tt.want)
			}
		})
	}
}

func TestSubmissionModeConflicts(t *testing.T) {
	for _, opts := range []Options{
		{Build: true, Evolve: true},
		{Build: true, Recipe: "custom"},
		{Evolve: true, RecipeFile: "custom.yaml"},
		{Recipe: "custom", RecipeFile: "custom.yaml"},
	} {
		opts.TenantID, opts.SWFURL = "tenant", "http://example.invalid"
		if err := opts.Validate(); err == nil {
			t.Fatalf("accepted conflicting options: %+v", opts)
		}
	}
}

func TestSubmissionType(t *testing.T) {
	for _, tt := range []struct {
		name    string
		opts    Options
		want    any
		wantErr bool
	}{
		{name: "default", want: "build"},
		{name: "build", opts: Options{Build: true}, want: "build"},
		{name: "evolve", opts: Options{Evolve: true}, want: "evolve"},
		{name: "explicit build name", opts: Options{Recipe: "build"}, want: "build"},
		{name: "explicit evolve name", opts: Options{Recipe: "evolve"}, want: "evolve"},
		{name: "matching type", opts: Options{Evolve: true, InputsJSON: `{"type":"evolve"}`}, want: "evolve"},
		{name: "conflicting type", opts: Options{InputsJSON: `{"type":"evolve"}`}, wantErr: true},
		{name: "conflicting evolve", opts: Options{Evolve: true, InputsJSON: `{"type":"build"}`}, wantErr: true},
		{name: "null type", opts: Options{InputsJSON: `{"type":null}`}, wantErr: true},
		{name: "numeric type", opts: Options{InputsJSON: `{"type":1}`}, wantErr: true},
		{name: "object type", opts: Options{InputsJSON: `{"type":{}}`}, wantErr: true},
		{name: "empty type", opts: Options{InputsJSON: `{"type":""}`}, wantErr: true},
		{name: "custom recipe", opts: Options{Recipe: "custom"}},
		{name: "custom file", opts: Options{RecipeFile: "build.yaml"}},
		{name: "explicit selector", opts: Options{Recipe: "git+https://example.test/repo.git//build.yaml@main"}},
		{name: "custom input preserved", opts: Options{Recipe: "custom", InputsJSON: `{"type":"review"}`}, want: "review"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.Prompt = "implement the feature"
			inputs, err := loadInputs(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "inputs.type must be") {
					t.Fatal(err)
				}
				return
			}
			if inputs["type"] != tt.want || inputs["prompt"] != tt.opts.Prompt {
				t.Fatalf("inputs = %#v, want type %v", inputs, tt.want)
			}
		})
	}
}

func TestSubmissionTypeFromInputsFile(t *testing.T) {
	for _, raw := range []string{`{"prompt":"do it","type":"evolve"}`, "prompt: do it\ntype: evolve\n"} {
		root := t.TempDir()
		path := filepath.Join(root, "inputs.yaml")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		inputs, err := loadInputs(Options{InputsFile: path, Evolve: true})
		if err != nil {
			t.Fatal(err)
		}
		if inputs["prompt"] != "do it" || inputs["type"] != "evolve" {
			t.Fatalf("inputs = %#v", inputs)
		}
		if _, err := loadInputs(Options{InputsFile: path}); err == nil {
			t.Fatal("accepted conflicting type from file")
		}
	}
}

func TestSubmissionPrompt(t *testing.T) {
	for _, tt := range []struct {
		name        string
		opts        Options
		stdin       string
		want        string
		wantErr     bool
		interactive bool
	}{
		{name: "positional", opts: Options{Prompt: "build it"}, want: "build it"},
		{name: "inputs", opts: Options{InputsJSON: `{"prompt":"from inputs"}`}, want: "from inputs"},
		{name: "interactive", stdin: "from terminal\r\nnext answer\n", want: "from terminal", interactive: true},
		{name: "eof with text", stdin: "from terminal", want: "from terminal", interactive: true},
		{name: "empty", opts: Options{PromptSet: true}, wantErr: true},
		{name: "whitespace", opts: Options{Prompt: " \n\t"}, wantErr: true},
		{name: "non string", opts: Options{InputsJSON: `{"prompt":42}`}, wantErr: true},
		{name: "null prompt", opts: Options{InputsJSON: `{"prompt":null}`}, wantErr: true},
		{name: "null inputs", opts: Options{InputsJSON: `null`, Prompt: "test"}, wantErr: true},
		{name: "conflict", opts: Options{InputsJSON: `{"prompt":"json"}`, Prompt: "arg"}, wantErr: true},
		{name: "empty interactive", stdin: "\n", interactive: true, wantErr: true},
		{name: "eof", interactive: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			opts := tt.opts
			opts.Stdin, opts.Stderr = strings.NewReader(tt.stdin), &stderr
			inputs, err := loadInputs(opts)
			if err == nil {
				err = requirePrompt(&opts, inputs)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && inputs["prompt"] != tt.want {
				t.Fatalf("prompt = %#v, want %q", inputs["prompt"], tt.want)
			}
			if strings.Contains(stderr.String(), "Prompt:") != tt.interactive {
				t.Fatalf("unexpected prompt output %q", stderr.String())
			}
			if tt.name == "interactive" {
				rest, err := io.ReadAll(opts.Stdin)
				if err != nil || string(rest) != "next answer\n" {
					t.Fatalf("lost subsequent input: %q, %v", rest, err)
				}
			}
		})
	}
}

func TestMissingPromptDoesNotReadNonTerminalStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var stderr bytes.Buffer
	err = requirePrompt(&Options{Stdin: r, Stderr: &stderr}, map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "stdin is not a terminal") {
		t.Fatalf("error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected interactive output: %q", stderr.String())
	}
}
