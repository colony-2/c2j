package template

import "github.com/colony-2/c2j/pkg/recipe"

// TransitionData is the transition metadata visible to a target state invocation.
type TransitionData struct {
	From    string                 `json:"from,omitempty"`
	To      string                 `json:"to,omitempty"`
	Payload map[string]interface{} `json:"payload"`
	Failure *recipe.RuntimeFailure `json:"failure,omitempty"`
}

func EmptyTransitionData() TransitionData {
	return TransitionData{Payload: map[string]interface{}{}}
}

func NewTransitionData(from string, to string, payload map[string]interface{}) TransitionData {
	return NewFailureTransitionData(from, to, payload, nil)
}

// NewFailureTransitionData takes ownership of its immutable payload and failure.
func NewFailureTransitionData(from string, to string, payload map[string]interface{}, failure *recipe.RuntimeFailure) TransitionData {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	return TransitionData{
		From:    from,
		To:      to,
		Payload: payload,
		Failure: failure,
	}
}

func (td TransitionData) Clone() TransitionData {
	return NewFailureTransitionData(td.From, td.To, cloneTemplateVars(td.Payload), td.Failure.Clone())
}

func (td TransitionData) AsMap() map[string]interface{} {
	payload := td.Payload
	if payload == nil {
		payload = map[string]interface{}{}
	}
	out := map[string]interface{}{
		"from":    td.From,
		"to":      td.To,
		"payload": payload,
	}
	if td.Failure != nil {
		out["failure"] = flattenTemplateValue(td.Failure)
	}
	return out
}
