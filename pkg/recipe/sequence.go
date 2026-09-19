package recipe

type SequenceData struct {
	Sequence NodeList               `yaml:"sequence"`
	Outputs  map[string]interface{} `yaml:"outputs,omitempty"`
}
