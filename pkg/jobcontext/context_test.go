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

func TestChildJobBrokerEnvRoundTrip(t *testing.T) {
	env := EnvForChildJobBroker(ChildJobBroker{
		Endpoint:  " http://127.0.0.1:1234/v1/child-jobs ",
		Token:     " token ",
		SessionID: " session ",
	})

	got, ok, err := ChildJobBrokerFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("ChildJobBrokerFromEnv(): %v", err)
	}
	if !ok {
		t.Fatal("ChildJobBrokerFromEnv() ok = false")
	}
	if got.Endpoint != "http://127.0.0.1:1234/v1/child-jobs" || got.Token != "token" || got.SessionID != "session" {
		t.Fatalf("unexpected broker env: %#v", got)
	}
}

func TestChildJobBrokerFromEnvRejectsPartialContext(t *testing.T) {
	_, ok, err := ChildJobBrokerFromEnv(func(key string) string {
		if key == ChildJobEndpointEnv {
			return "http://127.0.0.1:1234/v1/child-jobs"
		}
		return ""
	})
	if !ok {
		t.Fatal("ChildJobBrokerFromEnv() ok = false")
	}
	if err == nil {
		t.Fatal("expected partial broker context error")
	}
}
