package submitjob

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-isatty"
)

func readPromptFile(opts Options) (string, error) {
	path, err := absPathFromWorkingDir(opts.WorkingDir, opts.PromptFile)
	if err != nil {
		return "", fmt.Errorf("resolve prompt file: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt file: %w", err)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("prompt file must contain valid UTF-8 text")
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("prompt file must contain a non-empty prompt")
	}
	return string(raw), nil
}

func requirePrompt(opts *Options, inputs map[string]interface{}) error {
	if value, exists := inputs["prompt"]; exists {
		prompt, ok := value.(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return fmt.Errorf("prompt must be a non-empty string")
		}
		return nil
	}
	if f, ok := opts.Stdin.(*os.File); ok && !isatty.IsTerminal(f.Fd()) && !isatty.IsCygwinTerminal(f.Fd()) {
		return fmt.Errorf("a prompt is required: use c2j submit \"your prompt\", --prompt-file, or supply inputs.prompt; stdin is not a terminal")
	}
	if opts.Stdin == nil {
		return fmt.Errorf("a prompt is required")
	}
	if _, err := fmt.Fprint(opts.Stderr, "Prompt: "); err != nil {
		return err
	}
	prompt, err := readPromptLine(opts.Stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read prompt: %w", err)
	}
	prompt = strings.TrimSuffix(strings.TrimSuffix(prompt, "\n"), "\r")
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("prompt must be a non-empty string")
	}
	inputs["prompt"] = prompt
	return nil
}

// Read only the prompt line, leaving stdin (and its terminal identity) intact
// for submit --run's subsequent interactive requests.
func readPromptLine(reader io.Reader) (string, error) {
	var line strings.Builder
	var next [1]byte
	for {
		_, err := io.ReadFull(reader, next[:])
		if err != nil {
			return line.String(), err
		}
		if next[0] == '\n' {
			return line.String(), nil
		}
		line.WriteByte(next[0])
	}
}
