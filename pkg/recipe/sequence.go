package recipe

type SequenceData struct {
	Sequence NodeList               `yaml:"sequence,omitempty"`
	Outputs  map[string]interface{} `yaml:"outputs,omitempty"`
}
