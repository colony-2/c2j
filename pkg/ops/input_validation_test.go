package ops

import (
	"reflect"
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
