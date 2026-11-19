package main

import (
	"fmt"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/cmd"
	"github.com/colony-2/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/pkg/recipe"
)

func main() {
	code, err := cmd.Execute(buildinfo.Current())
	if err != nil {
		if message, ok := recipe.CompactErrorMessage(err); ok {
			fmt.Fprintln(os.Stderr, message)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}
	os.Exit(code)
}
