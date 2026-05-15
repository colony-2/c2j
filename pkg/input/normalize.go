package input

import "github.com/colony-2/c2j/pkg/input/formdefaults"

// NormalizeOutput stabilizes successful input outputs against the rendered form
// schema. Required fields without submitted values or explicit defaults fail
// instead of receiving invented values.
func NormalizeOutput(form Config, out Output) (Output, error) {
	normalized, err := formdefaults.NormalizeOutput(defaultConfigFromConfig(form), formdefaults.Output{
		Response:        out.Response,
		ResponsePresent: out.Response != nil,
		Fields:          out.Fields,
		UserID:          out.UserID,
		Metadata:        out.Metadata,
	})
	if err != nil {
		return Output{}, err
	}
	return Output{
		Response: normalized.Response,
		Fields:   normalized.Fields,
		UserID:   normalized.UserID,
		Metadata: normalized.Metadata,
	}, nil
}

func NormalizeOutputMap(opInput map[string]interface{}, opOutput map[string]interface{}) (map[string]interface{}, error) {
	return formdefaults.NormalizeOutputMap(opInput, opOutput)
}

func defaultConfigFromConfig(form Config) formdefaults.Config {
	out := formdefaults.Config{
		Question:   form.Question,
		Type:       string(form.Type),
		Default:    form.Default,
		HasDefault: form.Default != nil,
	}
	if form.Scale != nil {
		out.ScaleMin = form.Scale.Min
		out.HasScale = true
	}
	if len(form.Fields) > 0 {
		out.Fields = make([]formdefaults.Field, 0, len(form.Fields))
		for _, field := range form.Fields {
			out.Fields = append(out.Fields, defaultFieldFromFormField(field))
		}
	}
	return out
}

func defaultFieldFromFormField(field FormField) formdefaults.Field {
	out := formdefaults.Field{
		ID:         field.ID,
		Type:       string(field.Type),
		Required:   field.Required,
		Default:    field.Default,
		HasDefault: field.Default != nil,
	}
	if field.Scale != nil {
		out.ScaleMin = field.Scale.Min
		out.HasScale = true
	}
	return out
}
