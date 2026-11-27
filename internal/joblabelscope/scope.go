package joblabelscope

import (
	"context"

	"github.com/colony-2/c2j/pkg/joblabel"
)

const Builtin = "job_label_edit"

type EditFunc func(context.Context, joblabel.Edit) (joblabel.State, error)
type ExtensionSession func(context.Context, string) (map[string]string, func(), error)

type builtinKey struct{}
type extensionKey struct{}
type launchKey struct{}

type Extension struct {
	Selector string
	Edit     EditFunc
}

func WithBuiltin(ctx context.Context, edit EditFunc) context.Context {
	return context.WithValue(ctx, builtinKey{}, edit)
}

func BuiltinEdit(ctx context.Context) EditFunc {
	edit, _ := ctx.Value(builtinKey{}).(EditFunc)
	return edit
}

func WithExtension(ctx context.Context, grant Extension) context.Context {
	return context.WithValue(ctx, extensionKey{}, grant)
}

func ExtensionGrant(ctx context.Context) Extension {
	grant, _ := ctx.Value(extensionKey{}).(Extension)
	return grant
}

func WithLaunch(ctx context.Context, launch ExtensionSession) context.Context {
	return context.WithValue(ctx, launchKey{}, launch)
}

func Launch(ctx context.Context) ExtensionSession {
	launch, _ := ctx.Value(launchKey{}).(ExtensionSession)
	return launch
}
