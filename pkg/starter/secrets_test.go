package starter

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/stretchr/testify/require"
)

func TestSubmissionRejectsPlaintextSecretBeforeStoring(t *testing.T) {
	r, err := recipe.LoadRecipeFromString([]byte("id: secret\ninput_schema:\n  token: {type: string, secret: true, required: true}\nsequence: []\n"))
	require.NoError(t, err)
	submit := &captureSubmitter{}
	start := workflowctl.StartJob{TenantId: "tenant", RecipeName: "secret", Inputs: map[string]any{"token": "private-plaintext"}}
	_, err = StartRecipeJob(context.Background(), start, submit, *r)
	require.ErrorContains(t, err, "requires a reference")
	require.Nil(t, submit.job.Data)
	require.NotContains(t, err.Error(), "private-plaintext")
	start.Inputs["token"] = "${{ context.secrets.github_token }}"
	_, err = StartRecipeJob(context.Background(), start, submit, *r)
	require.NoError(t, err)
	raw, err := submit.job.Data.GetData()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"$c2j_secret":"github_token"`)
	require.Equal(t, "${{ context.secrets.github_token }}", start.Inputs["token"], "submission does not mutate caller inputs")
}
