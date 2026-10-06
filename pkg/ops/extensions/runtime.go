package extensions

import (
	"context"

	"github.com/colony-2/c2j/pkg/ops/process"
)

type RunRequest = process.RunRequest

func ExecuteProcess(ctx context.Context, req RunRequest) ([]byte, []byte, error) {
	return process.ExecuteProcess(ctx, req)
}

func buildProcessEnv(env map[string]string) []string {
	return process.BuildProcessEnv(env)
}

func buildProcessEnvMap(env map[string]string) map[string]string {
	return process.BuildProcessEnvMap(env)
}
