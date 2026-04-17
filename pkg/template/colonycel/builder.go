package colonycel

import (
	"github.com/colony-2/c2j/pkg/template/funcregistry"
)

type Options struct {
}

func NewBuilder(opts Options) *funcregistry.Builder {
	builder := funcregistry.NewBuilder().WithDefaults()
	RegisterArtifactFunctions(builder)
	return builder
}
