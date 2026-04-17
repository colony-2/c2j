package runjob

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/input"
)

func promptForInput(stdin io.Reader, stdout io.Writer, details *input.UserInputDetails) (input.FormResponse, error) {
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
			value, err := promptField(reader, stdout, field)
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
	value, err := promptQuestion(reader, stdout, question, fieldType, form.Options)
	if err != nil {
		return input.FormResponse{}, err
	}
	resp.Response = &value
	return resp, nil
}

func promptField(reader *bufio.Reader, stdout io.Writer, field input.APIFormField) (interface{}, error) {
	return promptQuestion(reader, stdout, field.Question, string(field.Type), field.Options)
}

func promptQuestion(reader *bufio.Reader, stdout io.Writer, question string, fieldType string, options *[]input.APIOption) (interface{}, error) {
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
	raw, err := reader.ReadString('\n')
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
