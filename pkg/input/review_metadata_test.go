package input

import (
	"context"
	"encoding/json"
	"fmt"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/input/openapi"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func metadataConfig() Config {
	zero := 0
	return Config{Kind: "review", Question: "Proceed?", Type: FieldTypeBoolean,
		Documents:        map[string]any{"brief": nil, "design": nil, "checks": nil},
		DocumentMetadata: map[string]DocumentMetadata{"brief": {Title: "Decision brief", Description: "Evidence", Role: "primary", Order: &zero, SourceDocumentIDs: []string{"design", "checks"}}}, OverviewDocumentID: "brief"}
}

func TestDocumentMetadataValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config)
		field  string
	}{
		{"valid", func(*Config) {}, ""},
		{"partial", func(c *Config) {
			c.DocumentMetadata = map[string]DocumentMetadata{"design": {}}
			c.OverviewDocumentID = "checks"
		}, ""},
		{"overview only", func(c *Config) { c.DocumentMetadata = nil }, ""},
		{"metadata only", func(c *Config) { c.OverviewDocumentID = "" }, ""},
		{"no documents", func(c *Config) { c.Documents = nil; c.DocumentMetadata = nil; c.OverviewDocumentID = "" }, ""},
		{"missing key", func(c *Config) { c.DocumentMetadata["missing"] = DocumentMetadata{} }, "document_metadata.missing"},
		{"missing overview", func(c *Config) { c.OverviewDocumentID = "Brief" }, "overview_document_id"},
		{"ordinary", func(c *Config) { c.Kind = ""; c.Documents = nil }, "document_metadata"},
		{"structured", func(c *Config) {
			c.Kind = ""
			c.Documents = nil
			c.Question = ""
			c.Type = ""
			c.ResponseSchema = map[string]any{}
		}, "document_metadata"},
		{"overview ordinary", func(c *Config) { c.Kind = ""; c.Documents = nil; c.DocumentMetadata = nil }, "overview_document_id"},
		{"empty ordinary", func(c *Config) {
			c.Kind = ""
			c.Documents = nil
			c.DocumentMetadata = map[string]DocumentMetadata{}
			c.OverviewDocumentID = ""
		}, ""},
		{"empty structured", func(c *Config) {
			*c = Config{ResponseSchema: map[string]any{}, DocumentMetadata: map[string]DocumentMetadata{}}
		}, ""},
		{"mixed presentation", func(c *Config) { c.Presentation = map[string]any{} }, "form"},
		{"multi field", func(c *Config) { c.Question = ""; c.Type = ""; c.Fields = reviewConfig().Fields }, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := metadataConfig()
			test.change(&c)
			err := c.ValidateOpInput()
			if test.field == "" {
				require.NoError(t, err)
			} else {
				var ve ValidationError
				require.ErrorAs(t, err, &ve)
				require.Contains(t, ve.Field, test.field)
			}
		})
	}
	for _, role := range []string{"", "primary", "supporting", "diagnostic", "Primary", "unknown"} {
		c := metadataConfig()
		m := c.DocumentMetadata["brief"]
		m.Role = role
		c.DocumentMetadata["brief"] = m
		err := c.ValidateOpInput()
		if role == "Primary" || role == "unknown" {
			require.ErrorContains(t, err, ".role")
		} else {
			require.NoError(t, err)
		}
	}
	for _, order := range []int{-1, 0, 1, 1000000, 1000001} {
		c := metadataConfig()
		m := c.DocumentMetadata["brief"]
		m.Order = &order
		c.DocumentMetadata["brief"] = m
		if order < 0 || order > 1000000 {
			require.ErrorContains(t, c.ValidateOpInput(), ".order")
		} else {
			require.NoError(t, c.ValidateOpInput())
		}
	}
	for _, text := range []struct {
		value string
		valid bool
	}{
		{"", true}, {strings.Repeat("界", 120), true}, {strings.Repeat("界", 121), false}, {" <b>e\u0301</b> ", true}, {string([]byte{0xff}), false},
	} {
		c := metadataConfig()
		m := c.DocumentMetadata["brief"]
		m.Title = text.value
		c.DocumentMetadata["brief"] = m
		if text.valid {
			require.NoError(t, c.ValidateOpInput())
		} else {
			require.ErrorContains(t, c.ValidateOpInput(), ".title")
		}
	}
	for _, n := range []int{300, 301} {
		c := metadataConfig()
		m := c.DocumentMetadata["brief"]
		m.Description = strings.Repeat("界", n)
		c.DocumentMetadata["brief"] = m
		if n == 300 {
			require.NoError(t, c.ValidateOpInput())
		} else {
			require.ErrorContains(t, c.ValidateOpInput(), ".description")
		}
	}
	for r := rune(0); r <= 127; r++ {
		if r >= 32 && r != 127 {
			continue
		}
		for _, field := range []string{"title", "description"} {
			c := metadataConfig()
			m := c.DocumentMetadata["brief"]
			if field == "title" {
				m.Title = string(r)
			} else {
				m.Description = string(r)
			}
			c.DocumentMetadata["brief"] = m
			require.ErrorContains(t, c.ValidateOpInput(), "."+field)
		}
	}
	for _, sources := range [][]string{nil, {}, {"design", "checks"}, {"design", "design"}, {"brief"}, {"missing"}} {
		c := metadataConfig()
		m := c.DocumentMetadata["brief"]
		m.SourceDocumentIDs = sources
		c.DocumentMetadata["brief"] = m
		if len(sources) > 0 && (sources[len(sources)-1] == "design" || sources[0] == "brief" || sources[0] == "missing") {
			require.ErrorContains(t, c.ValidateOpInput(), "source_document_ids")
		} else {
			require.NoError(t, c.ValidateOpInput())
		}
	}
	c := metadataConfig()
	for n := 0; n < 129; n++ {
		c.Documents[fmt.Sprint(n)] = nil
	}
	m := c.DocumentMetadata["brief"]
	m.SourceDocumentIDs = nil
	for n := 0; n < 129; n++ {
		m.SourceDocumentIDs = append(m.SourceDocumentIDs, fmt.Sprint(n))
		c.DocumentMetadata["brief"] = m
		if n < 128 {
			require.NoError(t, c.ValidateOpInput())
		} else {
			require.ErrorContains(t, c.ValidateOpInput(), "128")
		}
	}
	c = metadataConfig()
	c.DocumentMetadata["design"] = DocumentMetadata{SourceDocumentIDs: []string{"brief"}}
	require.NoError(t, c.ValidateOpInput(), "cross-document cycles require no traversal")
	for _, id := range []string{"Brief", " brief", "brief/../brief", "decision.md"} {
		c := metadataConfig()
		c.OverviewDocumentID = id
		require.Error(t, c.ValidateOpInput())
	}
	c = metadataConfig()
	c.Documents[" brief "] = nil
	c.OverviewDocumentID = " brief "
	require.NoError(t, c.ValidateOpInput())
}

func TestDocumentMetadataPayloadBoundaries(t *testing.T) {
	// Long opaque document IDs isolate the combined bound from text/list limits.
	for _, size := range []int{65535, 65536, 65537} {
		c := Config{Kind: "review", Question: "Proceed?", OverviewDocumentID: "x"}
		raw, err := json.Marshal(reviewDocumentPresentation{OverviewDocumentID: "x"})
		require.NoError(t, err)
		c.OverviewDocumentID = strings.Repeat("x", size-len(raw)+1)
		c.Documents = map[string]any{c.OverviewDocumentID: nil}
		got, err := json.Marshal(reviewDocumentPresentation{OverviewDocumentID: c.OverviewDocumentID})
		require.NoError(t, err)
		require.Len(t, got, size)
		if size <= 65536 {
			require.NoError(t, c.ValidateOpInput())
		} else {
			require.ErrorContains(t, c.ValidateOpInput(), "65536")
		}
	}
	c := metadataConfig()
	id := strings.Repeat("\"", 18000)
	c.Documents[id] = nil
	c.DocumentMetadata[id] = DocumentMetadata{SourceDocumentIDs: []string{"design"}}
	c.OverviewDocumentID = id
	require.ErrorContains(t, c.ValidateOpInput(), "65536", "escaped keys and overview both count")
}

func TestDocumentMetadataIngress(t *testing.T) {
	for _, value := range []any{0, 0.0, 1000000.0, -1, 0.5, 1000001, uint64(^uint64(0)), 1e30, json.Number("1.5"), "1"} {
		t.Run(fmt.Sprintf("%T-%v", value, value), func(t *testing.T) {
			data := map[string]any{"form": map[string]any{"kind": "review", "question": "Proceed?", "documents": map[string]any{"brief": nil}, "document_metadata": map[string]any{"brief": map[string]any{"order": value}}}}
			var input Input
			validationErr := ops.ValidateOpInputType(reflect.TypeFor[Input](), data, false)
			decodeErr := ops.DecodeWithJsonTags(data, &input)
			valid := reflect.DeepEqual(value, 0) || reflect.DeepEqual(value, 0.0) || reflect.DeepEqual(value, 1000000.0)
			if valid {
				require.NoError(t, validationErr)
				require.NoError(t, decodeErr)
				require.NotNil(t, input.Form.DocumentMetadata["brief"].Order)
			} else {
				// Range errors are producer validation; fractional/overflow values fail decoding itself.
				if decodeErr == nil {
					decodeErr = input.ValidateOpInput()
				}
				require.Error(t, validationErr)
				require.Error(t, decodeErr)
			}
		})
	}
	for _, field := range []string{"title", "description"} {
		bad := string([]byte{0xff})
		data := map[string]any{"form": map[string]any{"kind": "review", "question": "Proceed?", "documents": map[string]any{"brief": nil}, "document_metadata": map[string]any{"brief": map[string]any{field: bad}}}}
		var input Input
		require.Error(t, ops.DecodeWithJsonTags(data, &input))
		require.Error(t, ops.ValidateOpInputType(reflect.TypeFor[Input](), data, false))
		var m DocumentMetadata
		require.Error(t, json.Unmarshal([]byte("{\""+field+"\":\""+bad+"\"}"), &m))
	}
	var legacy Input
	require.NoError(t, ops.DecodeWithJsonTags(map[string]any{"form": map[string]any{"question": "Legacy"}}, &legacy))
	require.Empty(t, legacy.Form.DocumentMetadata)
}

func TestDocumentMetadataFreezeAndTransport(t *testing.T) {
	c := metadataConfig()
	ref := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 1, Name: "original.md", SizeBytes: 8})
	for id := range c.Documents {
		c.Documents[id] = ref
	}
	ctl := &clientControl{artifact: func(string, jobdb.ArtifactKey) jobdb.Artifact {
		return jobdb.NewArtifactFromBytes("original.md", []byte("original"))
	}}
	form, err := buildForm(ops.NewOpDependenciesBuilder().WithWorkflowControl(ctl).Build(), context.Background(), Input{Form: c})
	require.NoError(t, err)
	original, err := json.Marshal(form)
	require.NoError(t, err)
	m := c.DocumentMetadata["brief"]
	*m.Order = 99
	m.SourceDocumentIDs[0] = "changed"
	m.Title = "changed"
	c.DocumentMetadata["brief"] = m
	delete(c.Documents, "design")
	delete(c.DocumentMetadata, "brief")
	frozen, err := json.Marshal(form)
	require.NoError(t, err)
	require.Equal(t, original, frozen)
	require.Zero(t, *form.DocumentMetadata["brief"].Order)
	raw, err := json.Marshal(toOpenAPIInputFormConfig(form))
	require.NoError(t, err)
	var transport openapi.InputFormConfig
	require.NoError(t, json.Unmarshal(raw, &transport))
	require.Equal(t, "brief", *transport.OverviewDocumentId)
	require.Zero(t, *(*transport.DocumentMetadata)["brief"].Order)
	var decoded InputForm
	require.NoError(t, json.Unmarshal(original, &decoded))
	require.Equal(t, form.DocumentMetadata, decoded.DocumentMetadata)
	signal := InputRequestSignal{Form: form}
	signalRaw, err := json.Marshal(signal)
	require.NoError(t, err)
	var restored InputRequestSignal
	require.NoError(t, json.Unmarshal(signalRaw, &restored))
	require.Equal(t, form, restored.Form)
	form.DocumentMetadata = map[string]DocumentMetadata{"brief": {}}
	form.OverviewDocumentID = ""
	raw, err = json.Marshal(toOpenAPIInputFormConfig(form))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"brief":{}`)
	require.NotContains(t, string(raw), "overview_document_id")
	form.DocumentMetadata = nil
	raw, err = json.Marshal(toOpenAPIInputFormConfig(form))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "document_metadata")
	var legacy InputForm
	require.NoError(t, json.Unmarshal([]byte(`{"question":"legacy","response_schema":null}`), &legacy))
	require.Empty(t, legacy.RequestID)
	require.Empty(t, legacy.RequestedAt)
	require.Nil(t, legacy.Fallback)
	require.Nil(t, legacy.DocumentMetadata)
}

func TestDocumentMetadataSchemasAreAdditive(t *testing.T) {
	generator := workerops.NewDefaultSchemaGenerator()
	for _, typ := range []reflect.Type{reflect.TypeFor[Config](), reflect.TypeFor[InputForm](), reflect.TypeFor[DocumentMetadata]()} {
		schema, err := generator.GenerateSchema(typ)
		require.NoError(t, err)
		require.NotContains(t, schema.Required, "document_metadata")
		require.NotContains(t, schema.Required, "overview_document_id")
		if typ == reflect.TypeFor[DocumentMetadata]() {
			require.Empty(t, schema.Required)
			role, ok := schema.Properties.Get("role")
			require.True(t, ok)
			require.Equal(t, []any{"", "primary", "supporting", "diagnostic"}, role.Enum)
		}
	}
	source, err := os.ReadFile("api/openapi/recipe-input-api.yaml")
	require.NoError(t, err)
	var spec map[string]any
	require.NoError(t, yaml.Unmarshal(source, &spec))
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	require.Nil(t, schemas["DocumentMetadata"].(map[string]any)["required"])
	require.Nil(t, schemas["InputFormConfig"].(map[string]any)["required"])
	var legacy openapi.InputFormConfig
	require.NoError(t, json.Unmarshal([]byte(`{"question":"Legacy?","documents":{}}`), &legacy))
	require.Nil(t, legacy.DocumentMetadata)
	require.Nil(t, legacy.OverviewDocumentId)
}

// These are serialized producer/consumer contract examples, not UI assertions.
func TestReviewPresentationConsumerFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/review-presentation.json")
	require.NoError(t, err)
	var cases []struct {
		Name    string              `json:"name"`
		Form    InputForm           `json:"form"`
		Initial string              `json:"expected_initial_document_id"`
		Groups  map[string][]string `json:"expected_groups"`
	}
	require.NoError(t, json.Unmarshal(raw, &cases))
	require.Len(t, cases, 6)
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			docs := map[string]any{}
			for id, ref := range tc.Form.Documents {
				require.NoError(t, ref.Validate())
				docs[id] = ref
			}
			cfg := Config{Kind: "review", Question: tc.Form.Question, Documents: docs, DocumentMetadata: tc.Form.DocumentMetadata, OverviewDocumentID: tc.Form.OverviewDocumentID}
			require.NoError(t, cfg.ValidateOpInput())
			reached := map[string]bool{}
			for _, group := range []string{"primary", "supporting", "diagnostic", "neutral"} {
				for _, id := range tc.Groups[group] {
					require.Contains(t, docs, id)
					require.False(t, reached[id])
					reached[id] = true
					role := tc.Form.DocumentMetadata[id].Role
					if role == "" {
						role = "neutral"
					}
					require.Equal(t, group, role)
				}
			}
			require.Len(t, reached, len(docs))
			if len(docs) > 0 {
				require.Contains(t, docs, tc.Initial)
			} else {
				require.Empty(t, tc.Initial)
			}
			if tc.Form.OverviewDocumentID != "" {
				require.Equal(t, tc.Form.OverviewDocumentID, tc.Initial)
			}
			transport := toOpenAPIInputFormConfig(tc.Form)
			encoded, err := json.Marshal(transport)
			require.NoError(t, err)
			// Tolerant old consumers retain the original map without metadata types.
			var old struct {
				Documents map[string]json.RawMessage `json:"documents"`
				Question  string                     `json:"question"`
			}
			require.NoError(t, json.Unmarshal(encoded, &old))
			require.Len(t, old.Documents, len(docs))
			require.Equal(t, tc.Form.Question, old.Question)
		})
	}
}

func TestDocumentMetadataTypedUTF8AndLegacyBounds(t *testing.T) {
	bad := string([]byte{255})
	for _, value := range []any{DocumentMetadata{Title: bad}, &DocumentMetadata{Description: bad}, map[string]string{"title": bad}} {
		var metadata DocumentMetadata
		require.Error(t, metadata.DecodeFromMap(value))
	}
	c := Config{Kind: "review", Question: "Legacy", Documents: map[string]any{strings.Repeat("x", 70000): nil}}
	require.NoError(t, c.ValidateOpInput(), "presentation-size limit does not bound legacy document IDs")
	c = Config{Title: strings.Repeat("界", 1000), Fields: []FormField{{ID: "text", Type: FieldTypeParagraphText}}, Output: &Output{Fields: map[string]any{"text": strings.Repeat("x", 70000)}}}
	form, err := buildForm(ops.NewOpDependenciesBuilder().Build(), context.Background(), Input{Form: c})
	require.NoError(t, err)
	require.Equal(t, c.Title, form.Title)
	out, err := autoFillInputWithDeps(ops.NewOpDependenciesBuilder().Build(), context.Background(), form)
	require.NoError(t, err)
	require.Len(t, out.Fields["text"], 70000)
}

func TestInvalidDocumentMetadataPublishesNothing(t *testing.T) {
	deps := ops.NewOpDependenciesBuilder().Build()
	c := metadataConfig()
	c.Documents = map[string]any{"brief": map[string]any{"kind": "value", "name": "brief.md", "format": "text", "value": "original"}}
	c.DocumentMetadata["brief"] = DocumentMetadata{Role: "invalid"}
	form, err := buildForm(deps, context.Background(), Input{Form: c})
	require.ErrorContains(t, err, ".role")
	require.Empty(t, form.RequestID)
	require.Empty(t, deps.GetOutputArtifacts())
}

func TestReviewMetadataConfigJSONAndLegacyDocumentForms(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 1, Name: "original.md", SizeBytes: 8}
	ref := artifacts.NewStoredRef(key)
	ctl := &clientControl{artifact: func(string, jobdb.ArtifactKey) jobdb.Artifact {
		return jobdb.NewArtifactFromBytes("original.md", []byte("original"))
	}}
	for _, annotated := range []bool{false, true} {
		for _, value := range []any{ref, key, map[string]any{"jobId": "producer", "taskOrdinal": 1, "name": "original.md", "sizeBytes": 8}} {
			cfg := metadataConfig()
			for id := range cfg.Documents {
				cfg.Documents[id] = value
			}
			if !annotated {
				cfg.DocumentMetadata = nil
				cfg.OverviewDocumentID = ""
			}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			var restored Config
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Equal(t, cfg.DocumentMetadata, restored.DocumentMetadata)
			require.Equal(t, cfg.OverviewDocumentID, restored.OverviewDocumentID)
			form, err := buildForm(ops.NewOpDependenciesBuilder().WithWorkflowControl(ctl).Build(), context.Background(), Input{Form: restored})
			require.NoError(t, err)
			require.Equal(t, ref, form.Documents["brief"])
		}
		for _, value := range []any{"/tmp/original.md", artifacts.NewExternalRef("original.md", "https://example.invalid/original.md", false)} {
			cfg := metadataConfig()
			for id := range cfg.Documents {
				cfg.Documents[id] = value
			}
			if !annotated {
				cfg.DocumentMetadata = nil
				cfg.OverviewDocumentID = ""
			}
			_, err := buildForm(ops.NewOpDependenciesBuilder().WithWorkflowControl(ctl).Build(), context.Background(), Input{Form: cfg})
			require.Error(t, err)
		}
	}
}
