package template

// TransitionData is the transition metadata visible to a target state invocation.
type TransitionData struct {
	From    string                 `json:"from,omitempty"`
	To      string                 `json:"to,omitempty"`
	Payload map[string]interface{} `json:"payload"`
}

func EmptyTransitionData() TransitionData {
	return TransitionData{Payload: map[string]interface{}{}}
}

func NewTransitionData(from string, to string, payload map[string]interface{}) TransitionData {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	return TransitionData{
		From:    from,
		To:      to,
		Payload: cloneTemplateVars(payload),
	}
}

func (td TransitionData) Clone() TransitionData {
	return NewTransitionData(td.From, td.To, td.Payload)
}

func (td TransitionData) AsMap() map[string]interface{} {
	payload := td.Payload
	if payload == nil {
		payload = map[string]interface{}{}
	}
	return map[string]interface{}{
		"from":    td.From,
		"to":      td.To,
		"payload": cloneTemplateVars(payload),
	}
}
