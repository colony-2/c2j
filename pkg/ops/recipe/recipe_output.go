package recipe

import (
	"encoding/json"
	"fmt"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type recipeJobOutput struct {
	Outputs            map[string]interface{}
	Artifacts          map[string]recipeartifacts.Ref
	OutputAvailable    bool
	ArtifactsAvailable bool
}

func decodeRecipeJobOutput(deps ops.OpDependencies, data jobdb.JobData) (recipeJobOutput, error) {
	out := recipeJobOutput{
		Outputs:   map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{},
	}
	if data == nil {
		return out, nil
	}

	rawData, err := data.GetData()
	if err != nil {
		return recipeJobOutput{}, err
	}
	if len(rawData) > 0 {
		out.OutputAvailable = true
		var raw map[string]interface{}
		if err := json.Unmarshal(rawData, &raw); err != nil {
			return recipeJobOutput{}, err
		}
		if wrapped, ok := raw["output"]; ok {
			if cast, ok := wrapped.(map[string]interface{}); ok {
				out.Outputs = cast
			}
		} else {
			out.Outputs = raw
		}
		if wrapped, ok := raw["artifact_refs"]; ok {
			buf, err := json.Marshal(wrapped)
			if err != nil {
				return recipeJobOutput{}, err
			}
			if err := json.Unmarshal(buf, &out.Artifacts); err != nil {
				return recipeJobOutput{}, err
			}
			if len(out.Artifacts) > 0 {
				out.ArtifactsAvailable = true
			}
		}
	}

	artifacts, err := data.GetArtifacts()
	if err != nil {
		return recipeJobOutput{}, err
	}
	if len(artifacts) > 0 {
		out.ArtifactsAvailable = true
	}
	for _, artifact := range artifacts {
		if deps != nil {
			if err := deps.AddOutputArtifact(artifact); err != nil {
				return recipeJobOutput{}, err
			}
		}
		ref, err := recipeartifacts.RefFromArtifact(artifact)
		if err != nil {
			continue
		}
		name := ref.NameValue()
		if name == "" {
			name = artifact.Name()
		}
		if name == "" {
			return recipeJobOutput{}, fmt.Errorf("artifact ref has empty name")
		}
		out.Artifacts[name] = ref
	}
	for name, artifactRef := range out.Artifacts {
		if artifactRef.External == nil {
			continue
		}
		if deps != nil {
			if err := deps.AddExternalArtifact(name, artifactRef.External.URL, artifactRef.External.Expand); err != nil {
				return recipeJobOutput{}, err
			}
		}
	}
	return out, nil
}
