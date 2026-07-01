package jobcontext

import "testing"

func TestCurrentEnvRoundTrip(t *testing.T) {
	current := Current{
		TenantID:           " tenant ",
		JobID:              " job ",
		JobType:            "recipe",
		OpType:             "command_execution",
		OpStep:             "command_execution",
		OpTaskType:         "command_execution:command_execution",
		CellName:           "alpha",
		RepositorySource:   "github.com/acme/alpha",
		GitRef:             "main",
		InvocationPath:     "root/step",
		InvocationSequence: 7,
		InvocationHash:     "abc123",
	}

	env := EnvForCurrent(current)
	got, ok, err := CurrentFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("CurrentFromEnv(): %v", err)
	}
	if !ok {
		t.Fatalf("CurrentFromEnv() ok = false")
	}
	if got.TenantID != "tenant" || got.JobID != "job" || got.InvocationSequence != 7 || got.InvocationHash != "abc123" {
		t.Fatalf("unexpected current context: %#v", got)
	}
	if env[TenantIDEnv] != "tenant" {
		t.Fatalf("%s = %q", TenantIDEnv, env[TenantIDEnv])
	}
}

func TestCurrentFromEnvRejectsPartialContext(t *testing.T) {
	_, ok, err := CurrentFromEnv(func(key string) string {
		if key == CurrentInvocationHashEnv {
			return "abc123"
		}
		return ""
	})
	if !ok {
		t.Fatalf("CurrentFromEnv() ok = false")
	}
	if err == nil {
		t.Fatalf("expected partial context error")
	}
}

func TestMergeProtectedEnvWins(t *testing.T) {
	got := MergeProtectedEnv(
		map[string]string{CurrentJobIDEnv: "user", "OTHER": "x"},
		map[string]string{CurrentJobIDEnv: "system"},
	)
	if got[CurrentJobIDEnv] != "system" || got["OTHER"] != "x" {
		t.Fatalf("unexpected merged env: %#v", got)
	}
}
