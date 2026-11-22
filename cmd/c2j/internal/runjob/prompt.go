package runjob

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/input"
)

func promptForInput(ctx context.Context, stdin io.Reader, stdout io.Writer, details *input.UserInputDetails) (input.FormResponse, error) {
	if err := ctx.Err(); err != nil {
		return input.FormResponse{}, err
	}
	reader := bufio.NewReader(stdin)
	form := details.Form

	resp := input.FormResponse{
		Fields: map[string]interface{}{},
	}

	if form.Fields != nil && len(*form.Fields) > 0 {
		if form.Title != nil && strings.TrimSpace(*form.Title) != "" {
			fmt.Fprintf(stdout, "\n%s\n", strings.TrimSpace(*form.Title))
		}
		for _, field := range *form.Fields {
			value, err := promptField(ctx, reader, stdout, field)
			if err != nil {
				return input.FormResponse{}, err
			}
			resp.Fields[field.Id] = value
		}
		return resp, nil
	}

	question := ""
	if form.Question != nil {
		question = strings.TrimSpace(*form.Question)
	}
	fieldType := ""
	if form.Type != nil {
		fieldType = string(*form.Type)
	}
	value, err := promptQuestion(ctx, reader, stdout, question, fieldType, form.Options)
	if err != nil {
		return input.FormResponse{}, err
	}
	resp.Response = &value
	return resp, nil
}

func promptField(ctx context.Context, reader *bufio.Reader, stdout io.Writer, field input.APIFormField) (interface{}, error) {
	return promptQuestion(ctx, reader, stdout, field.Question, string(field.Type), field.Options)
}

func promptQuestion(ctx context.Context, reader *bufio.Reader, stdout io.Writer, question string, fieldType string, options *[]input.APIOption) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fmt.Fprintf(stdout, "\n%s\n", strings.TrimSpace(question))
	if options != nil && len(*options) > 0 {
		for i, option := range *options {
			label := option.Value
			if option.Label != nil && strings.TrimSpace(*option.Label) != "" {
				label = *option.Label
			}
			fmt.Fprintf(stdout, "  %d. %s\n", i+1, label)
		}
	}
	fmt.Fprint(stdout, "> ")
	raw, err := readPromptLine(ctx, reader)
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if options != nil && len(*options) > 0 {
		if selected, ok := resolveOptionSelection(raw, *options, fieldType); ok {
			return selected, nil
		}
	}
	return parsePromptValue(fieldType, raw), nil
}

func readPromptLine(ctx context.Context, reader *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	// io.Reader has no cancellation contract, and stdin belongs to the caller.
	// Isolate only the read so cancellation can return without closing stdin or
	// allowing background prompt output or response submission. A blocked read
	// finishes when the caller supplies input or closes its reader; the buffered
	// channel lets it finish even after this wait has been canceled.
	ready := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ready <- result{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case read := <-ready:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return read.line, read.err
	}
}

func resolveOptionSelection(raw string, options []input.APIOption, fieldType string) (interface{}, bool) {
	if raw == "" {
		return nil, false
	}
	index, err := strconv.Atoi(raw)
	if err != nil || index < 1 || index > len(options) {
		return nil, false
	}
	selected := options[index-1].Value
	if fieldType == string(input.APIFieldTypeCheckboxes) {
		return []string{selected}, true
	}
	return selected, true
}

func parsePromptValue(fieldType string, raw string) interface{} {
	switch fieldType {
	case string(input.APIFieldTypeCheckboxes):
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	case string(input.APIFieldTypeLinearScale):
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
		return raw
	default:
		return raw
	}
}
