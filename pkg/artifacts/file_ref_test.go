package artifacts

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestArtifactFileReferenceValidation(t *testing.T) {
	stored := `{"kind":"stored","stored":{"key":{"jobId":"producer","taskOrdinal":0,"name":"../same","sizeBytes":-1}}}`
	external := `{"kind":"external","name":"prefix","external":{"url":"file:///tmp/prefix"}}`
	for _, v := range []any{json.RawMessage(stored), json.RawMessage(external), NewStoredRef(jobdb.ArtifactKey{JobId: "p", TaskOrdinal: 0, Name: "x", SizeBytes: -1}), NewExternalRef("x", "https://example.test/x", false), NewStoredRef(jobdb.ArtifactKey{JobId: "max", TaskOrdinal: 9223372036854775807, Name: "x", SizeBytes: 9223372036854775807}), json.RawMessage(`{"kind":"stored","stored":{"key":{"jobId":"p","taskOrdinal":1.0,"name":"x","sizeBytes":1e2}}}`)} {
		_, err := ParseFileRef(v)
		require.NoError(t, err)
	}
	for name, raw := range map[string]string{
		"null": `null`, "string": `"/tmp/x"`, "url string": `"https://example.test/x"`, "array": `[]`, "empty": `{}`,
		"descriptor": `{"ref":` + external + `,"path":"/tmp/x"}`,
		"value":      `{"kind":"value","format":"text","value":"body"}`, "attached": `{"kind":"attached","name":"x"}`,
		"set": `{"kind":"set","items":[]}`, "unknown member": `{"kind":"external","name":"x","external":{"url":"file:///tmp/x"},"body":"x"}`,
		"mixed":          `{"kind":"stored","stored":{"key":{"jobId":"p","taskOrdinal":0,"name":"x","sizeBytes":0}},"external":{}}`,
		"stored missing": `{"kind":"stored"}`, "key null": `{"kind":"stored","stored":{"key":null}}`,
		"ordinal missing":    `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","sizeBytes":0}}}`,
		"size missing":       `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":0}}}`,
		"ordinal fractional": `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":1.1,"sizeBytes":0}}}`,
		"ordinal overflow":   `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":9223372036854775808,"sizeBytes":0}}}`,
		"ordinal negative":   `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":-1,"sizeBytes":0}}}`,
		"ordinal string":     `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":"0","sizeBytes":0}}}`,
		"size negative":      `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":0,"sizeBytes":-2}}}`,
		"size null":          `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":0,"sizeBytes":null}}}`,
		"job empty":          `{"kind":"stored","stored":{"key":{"jobId":"","name":"x","taskOrdinal":0,"sizeBytes":0}}}`,
		"key extra":          `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":0,"sizeBytes":0,"tenant":"other"}}}`,
		"payload extra":      `{"kind":"stored","stored":{"key":{"jobId":"p","name":"x","taskOrdinal":0,"sizeBytes":0},"other":true}}`,
		"name null":          `{"kind":"external","name":null,"external":{"url":"file:///tmp/x"}}`,
		"external no name":   `{"kind":"external","external":{"url":"file:///tmp/x"}}`,
		"external null":      `{"kind":"external","name":"x","external":null}`,
		"expand":             `{"kind":"external","name":"x","external":{"url":"file:///tmp/x","expand":true}}`,
		"expand null":        `{"kind":"external","name":"x","external":{"url":"file:///tmp/x","expand":null}}`,
		"expand object":      `{"kind":"external","name":"x","external":{"url":"file:///tmp/x","expand":{}}}`,
		"external extra":     `{"kind":"external","name":"x","external":{"url":"file:///tmp/x","extra":false}}`,
	} {
		t.Run(name, func(t *testing.T) { _, err := ParseFileRef(json.RawMessage(raw)); require.Error(t, err) })
	}
}

func TestArtifactFileURLs(t *testing.T) {
	for _, u := range []string{"file:///tmp/a%20b", "file://localhost/tmp/a", "http://example.test/path?q=1", "https://example.test/"} {
		_, err := FileRefURL(u)
		require.NoError(t, err)
	}
	for _, u := range []string{"", "/tmp/file", "ftp://example.test/x", "file:relative", "file://remote/tmp/x", "file:///tmp/x?q=1", "file:///tmp/x?", "file:///tmp/x#", "file:///tmp/x#part", "https:///x", "http:", "file://user@localhost/tmp/x"} {
		t.Run(u, func(t *testing.T) { _, err := FileRefURL(u); require.Error(t, err) })
	}
}
