package template

import "github.com/colony-2/c2j/pkg/template/internal/celnative"

func nativeTemplateResult(value any) any { return celnative.Value(value) }
