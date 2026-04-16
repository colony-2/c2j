package recipe

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	"github.com/colony-2/swf-go/pkg/swf"
)

func TestStartJobsEmpty(t *testing.T) {
	ctl := &fakeWorkflowControl{}
	_, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{}, ctl, "github.com/acme/demo", "main", nil, "")
	if err == nil {
		t.Fatal("expected error for empty recipes")
	}
}

func TestStartJobsSingle(t *testing.T) {
	ctl := &fakeWorkflowControl{}
	recipes := []SingleRecipe{{
		Name:   "child",
		Inputs: map[string]interface{}{"value": "ok"},
		Git: SingleRecipeGit{
			BaseRepo: "github.com/acme/demo",
			BaseRef:  "main",
		},
	}}
	keys, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{NodePath: "node", InvokeSeq: 1}, ctl, "github.com/acme/demo", "main", recipes, "git-ref")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if len(ctl.startRequests) != 1 {
		t.Fatalf("expected 1 StartJob call, got %d", len(ctl.startRequests))
	}
	if ctl.startSawTx[0] {
		t.Fatal("did not expect transaction for single job")
	}
	if got := ctl.startRequests[0].RecipeName; got != "git+https://github.com/acme/demo.git//.c2j/recipes/child.yaml@main" {
		t.Fatalf("RecipeName = %q", got)
	}
}

func TestStartJobsMultipleRunsSequentially(t *testing.T) {
	ctl := &fakeWorkflowControl{}
	recipes := []SingleRecipe{
		{
			Name: "child-a",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
		{
			Name: "child-b",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
	}
	keys, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{NodePath: "node", InvokeSeq: 1}, ctl, "github.com/acme/demo", "main", recipes, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	for i, sawTx := range ctl.startSawTx {
		if sawTx {
			t.Fatalf("did not expect transaction context for call %d", i)
		}
	}
}

func TestStartJobsMultipleErrorReturnsNoKeys(t *testing.T) {
	ctl := &fakeWorkflowControl{
		startErrs: []error{nil, errors.New("boom")},
	}
	recipes := []SingleRecipe{
		{
			Name: "child-a",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
		{
			Name: "child-b",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
	}
	keys, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{NodePath: "node", InvokeSeq: 1}, ctl, "github.com/acme/demo", "main", recipes, "")
	if err == nil {
		t.Fatal("expected error")
	}
	if keys != nil {
		t.Fatalf("expected nil keys on error, got %v", keys)
	}
}
