package extensions

import (
	"context"

	"github.com/colony-2/c2j/pkg/ops/process"
)

const (
	SandboxTypeNone = process.SandboxTypeNone
	SandboxTypeShai = process.SandboxTypeShai
)

type SandboxInput = process.SandboxInput
type SandboxPathConfig = process.SandboxPathConfig
type SandboxPathMapping = process.SandboxPathMapping
type RunRequest = process.RunRequest

func ParseSandboxInput(raw interface{}) (*SandboxInput, error) {
	return process.ParseSandboxInput(raw)
}

func ExecuteProcess(ctx context.Context, req RunRequest) ([]byte, []byte, error) {
	return process.ExecuteProcess(ctx, req)
}

func buildProcessEnv(env map[string]string) []string {
	return process.BuildProcessEnv(env)
}

func buildProcessEnvMap(env map[string]string) map[string]string {
	return process.BuildProcessEnvMap(env)
}
