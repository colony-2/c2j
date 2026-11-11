package recipetest

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"

	"github.com/colony-2/c2j/internal/preparedtest"
	"github.com/colony-2/c2j/pkg/objects"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type PreparedEvidence struct {
	Objects      map[string]map[string]RetainedContent `json:"objects"`
	Artifacts    map[string]RetainedContent            `json:"artifacts"`
	ArtifactKeys map[string]jobdb.ArtifactKey          `json:"artifact_keys"`
	Tools        map[string]RetainedContent            `json:"tools"`
}

func capturePreparedEvidence(ctx context.Context, deps coreops.OpDependencies, in preparedtest.Invocation, tools []string, limit int) (*PreparedEvidence, error) {
	evidence := &PreparedEvidence{Objects: map[string]map[string]RetainedContent{}, Artifacts: map[string]RetainedContent{}, ArtifactKeys: map[string]jobdb.ArtifactKey{}, Tools: map[string]RetainedContent{}}
	retain := func(data []byte) (RetainedContent, error) {
		if len(data) > limit {
			return RetainedContent{}, fmt.Errorf("prepared content exceeds evidence limit")
		}
		safe := secrets.RedactorFromContext(ctx).Redact(string(data))
		return RetainedContent{SHA256: contentDigest(data), ContentBase64: base64.StdEncoding.EncodeToString([]byte(safe)), Redacted: safe != string(data)}, nil
	}
	read := func(stream io.ReadCloser) (RetainedContent, error) {
		defer stream.Close()
		data, err := io.ReadAll(io.LimitReader(stream, int64(limit)+1))
		if err != nil {
			return RetainedContent{}, err
		}
		return retain(data)
	}
	for name, value := range in.Inputs {
		object, ok := value.(*objects.Snapshot)
		if !ok {
			continue
		}
		evidence.Objects[name] = map[string]RetainedContent{}
		for part, path := range object.Files {
			stream, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			content, err := read(stream)
			if err != nil {
				return nil, err
			}
			evidence.Objects[name][part] = content
		}
	}

	for name, binding := range preparedtest.Bindings(ctx) {
		if binding.Ref.Stored == nil {
			return nil, fmt.Errorf("prepared capture requires stored artifact identity for %s", name)
		}
		path, err := fixturePath(binding.Inbox, name)
		if err != nil {
			return nil, err
		}
		stream, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		content, err := read(stream)
		if err != nil {
			return nil, err
		}
		evidence.Artifacts[name] = content
		evidence.ArtifactKeys[name] = binding.Ref.Stored.Key
	}
	for _, tool := range tools {
		stdout, _, err := process.ExecuteProcess(ctx, process.RunRequest{Command: []string{tool, "--version"}})
		if err != nil {
			return nil, fmt.Errorf("prepared tool probe %s: %w", tool, err)
		}
		content, err := retain(stdout)
		if err != nil {
			return nil, err
		}
		evidence.Tools[tool] = content
	}
	return evidence, nil
}
