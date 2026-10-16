package ops

import (
	"github.com/colony-2/c2j/pkg/contextual"
	"reflect"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestMaterializedOptionalSecretValidation(t *testing.T) {
	type required struct {
		Token string `json:"token" secret:"true" validate:"required"`
	}
	type optional struct {
		Token *string `json:"token" secret:"true"`
	}
	input := map[string]any{"token": secrets.Reference{Name: "token", OmitIfUnavailable: true}}
	require.NoError(t, ValidateOpInputType(reflect.TypeFor[required](), input, false))
	s := secrets.NewSession()
	defer s.Close()
	got, err := secrets.MaterializeInput(s, reflect.TypeFor[required](), input)
	require.NoError(t, err)
	require.Error(t, ValidateMaterializedOpInputType(reflect.TypeFor[required](), got.(map[string]any)))
	require.NoError(t, ValidateMaterializedOpInputType(reflect.TypeFor[optional](), got.(map[string]any)))
}

func TestGitExecutionReferenceValidation(t *testing.T) {
	type input struct {
		Hash string `json:"hash" validate:"required,hexadecimal,len=40"`
	}
	value := map[string]any{"hash": contextual.GitExecutionBaseSentinel}
	require.NoError(t, ValidateOpInputType(reflect.TypeFor[input](), value, false))
	require.Equal(t, contextual.GitExecutionBaseSentinel, value["hash"], "validation must not replace the durable reference")
	require.Error(t, ValidateMaterializedOpInputType(reflect.TypeFor[input](), value))
	require.Error(t, ValidateOpInputType(reflect.TypeFor[input](), map[string]any{"hash": "bogus"}, false))
	require.NoError(t, ValidateMaterializedOpInputType(reflect.TypeFor[input](), map[string]any{"hash": strings.Repeat("a", 40)}))
}
