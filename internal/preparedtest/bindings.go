package preparedtest

import (
	"context"
	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
)

type Binding struct {
	Ref   recipeartifacts.Ref
	Inbox string
}
type bindingsKey struct{}

func WithBindings(ctx context.Context, refs map[string]recipeartifacts.Ref, inbox string) context.Context {
	bindings := map[string]Binding{}
	for name, ref := range refs {
		bindings[name] = Binding{Ref: ref, Inbox: inbox}
	}
	return context.WithValue(ctx, bindingsKey{}, bindings)
}
func Bindings(ctx context.Context) map[string]Binding {
	bindings, _ := ctx.Value(bindingsKey{}).(map[string]Binding)
	return bindings
}
