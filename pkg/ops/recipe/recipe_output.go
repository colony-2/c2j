package recipe

import (
	"encoding/json"
	"fmt"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type recipeJobOutput struct {
	GitResult          contextual.GitCommitContext
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
		if git, ok := raw["git"]; ok {
			buf, err := json.Marshal(git)
			if err != nil {
				return recipeJobOutput{}, err
			}
			if err = json.Unmarshal(buf, &out.GitResult); err != nil {
				return recipeJobOutput{}, err
			}
		}
		if wrapped, ok := raw["output"]; ok {
			if cast, ok := wrapped.(map[string]interface{}); ok {
				out.Outputs = cast
			} else if wrapped != nil {
				return recipeJobOutput{}, fmt.Errorf("recipe result output must be an object")
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
		if artifact != nil && objects.IsInternalArtifact(artifact.Name()) {
			continue
		}
		// A child's snapshot belongs to that child, even when the awaiting
		// node uses the implicit root workspace. Preserve its reference below,
		// but never offer it as the awaiting task's automatic Git state.
		// Git diagnostics also belong to the producing task. Retain the child
		// reference without colliding with this task's own stderr artifact.
		internalGit := artifact.Name() == gitstate.ThinPackArtifactName || artifact.Name() == "diff_from_parent.diff" || artifact.Name() == "diff_from_base.diff" || artifact.Name() == gitprofile.StderrArtifactName
		if deps != nil && !internalGit {
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
		if artifactRef.Kind == recipeartifacts.RefKindAttached {
			return recipeJobOutput{}, fmt.Errorf("missing result artifact %q", name)
		}
		if artifactRef.External == nil {
			continue
		}
		if deps != nil {
			if err := deps.AddExternalArtifact(name, artifactRef.External.URL, artifactRef.External.Expand); err != nil {
				return recipeJobOutput{}, err
			}
		}
	}
	// Raw recipe outputs retain the declaration as well as the public artifact
	// map. Resolve transport markers there before returning to the parent.
	if refs, ok := out.Outputs["artifact_refs"].(map[string]any); ok {
		for name, value := range refs {
			if ref, ok := value.(map[string]any); ok && ref["kind"] == string(recipeartifacts.RefKindAttached) {
				refs[name] = out.Artifacts[name]
			}
		}
	}
	return out, nil
}
