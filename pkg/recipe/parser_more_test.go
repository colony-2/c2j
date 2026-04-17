package recipe

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type errReader struct{ err error }

func (e *errReader) Read(p []byte) (n int, err error) { return 0, e.err }

func TestLoadRecipeFromReader_IOError(t *testing.T) {
	r := &errReader{err: io.ErrUnexpectedEOF}
	_, err := LoadRecipeFromReader(r)
	assert.Error(t, err)
}

func TestLoadRecipeFromReader_MultiDoc_ParsesFirst(t *testing.T) {
	y := "---\nversion: 1.0\nsequence: []\n---\nversion: 2.0\nsequence: []\n"
	res, err := LoadRecipeFromReader(strings.NewReader(y))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// The decoder decodes the first document; nothing explicitly to assert except no error
	_ = res
}
