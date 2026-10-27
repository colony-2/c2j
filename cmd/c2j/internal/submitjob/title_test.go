package submitjob

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTitleInputMerge(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     Options
		want     any
		conflict bool
	}{
		{name: "absent"},
		{name: "flag", opts: Options{Title: "  Improve café\nUI  "}, want: "  Improve café\nUI  "},
		{name: "empty flag", opts: Options{TitleSet: true}, want: ""},
		{name: "json", opts: Options{InputsJSON: `{"title":"from json"}`}, want: "from json"},
		{name: "non-string input preserved", opts: Options{InputsJSON: `{"title":42}`}, want: float64(42)},
		{name: "duplicate", opts: Options{Title: "flag", InputsJSON: `{"title":"json"}`}, conflict: true},
		{name: "duplicate empty flag", opts: Options{TitleSet: true, InputsJSON: `{"title":"json"}`}, conflict: true},
		{name: "duplicate null input", opts: Options{Title: "flag", InputsJSON: `{"title":null}`}, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Prompt = "unchanged prompt"
			inputs, err := loadInputs(tc.opts)
			if tc.conflict {
				require.ErrorContains(t, err, "title was provided both")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, inputs["title"])
			if tc.want == nil {
				require.NotContains(t, inputs, "title")
			}
			require.Equal(t, "unchanged prompt", inputs["prompt"])
			require.Equal(t, "build", inputs["type"])
		})
	}
	for _, content := range []string{`{"title":"file title"}`, "title: file title\n"} {
		path := filepath.Join(t.TempDir(), "inputs.yaml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0600))
		opts := Options{InputsFile: path, Prompt: "prompt"}
		inputs, err := loadInputs(opts)
		require.NoError(t, err)
		require.Equal(t, "file title", inputs["title"])
		opts.Title = "flag title"
		_, err = loadInputs(opts)
		require.ErrorContains(t, err, "title was provided both")
	}
}
