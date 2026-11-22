package runjob

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/input"
	"github.com/stretchr/testify/require"
)

func TestPromptInputParsing(t *testing.T) {
	for _, test := range []struct {
		name, form, answers string
		response            any
		fields              map[string]any
	}{
		{
			name:    "single-question",
			form:    `{"question":"Answer?","type":"short_answer"}`,
			answers: "  answer  \n", response: "answer",
		},
		{
			name:    "option-selection",
			form:    `{"question":"Choose","type":"checkboxes","options":[{"value":"first"},{"value":"second"}]}`,
			answers: "2\n", response: []string{"second"},
		},
		{
			name:    "multiple-fields",
			form:    `{"title":"Questions","fields":[{"id":"scale","question":"Rating?","type":"linear_scale"},{"id":"choices","question":"Choices?","type":"checkboxes"}]}`,
			answers: "4\n one, two \n", fields: map[string]any{"scale": 4, "choices": []string{"one", "two"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var details input.UserInputDetails
			require.NoError(t, json.Unmarshal([]byte(`{"form":`+test.form+`}`), &details))
			var stdout bytes.Buffer
			response, err := promptForInput(context.Background(), strings.NewReader(test.answers), &stdout, &details)
			require.NoError(t, err)
			if test.fields != nil {
				require.Equal(t, test.fields, response.Fields)
				require.Nil(t, response.Response)
			} else {
				require.NotNil(t, response.Response)
				require.Equal(t, test.response, *response.Response)
			}
			require.Contains(t, stdout.String(), "> ")
		})
	}
}

func TestPromptReadError(t *testing.T) {
	var stdout bytes.Buffer
	response, err := promptForInput(context.Background(), strings.NewReader("partial"), &stdout, &input.UserInputDetails{})
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, input.FormResponse{}, response)
}

func TestPromptAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	response, err := promptForInput(ctx, strings.NewReader("answer\n"), &stdout, &input.UserInputDetails{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, input.FormResponse{}, response)
	require.Empty(t, stdout.String())
}

func TestPromptCancellationDiscardsPartialForm(t *testing.T) {
	var details input.UserInputDetails
	require.NoError(t, json.Unmarshal([]byte(`{"form":{"fields":[{"id":"first","question":"First?","type":"short_answer"},{"id":"second","question":"Second?","type":"short_answer"}]}}`), &details))
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	barrier := &healthPromptBarrier{entered: make(chan struct{}), reader: reader}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout bytes.Buffer
	type result struct {
		response input.FormResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := promptForInput(ctx, io.MultiReader(strings.NewReader("first answer\n"), barrier), &stdout, &details)
		done <- result{response: response, err: err}
	}()
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("second field not reached")
	}
	cancel()
	select {
	case result := <-done:
		require.ErrorIs(t, result.err, context.Canceled)
		require.Equal(t, input.FormResponse{}, result.response, "partial forms must not be submitted")
	case <-time.After(time.Second):
		t.Fatal("prompt did not stop without more input")
	}
	require.Contains(t, stdout.String(), "Second?")
}
