package main

import (
	"fmt"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/cmd/c2j/internal/cmd"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	info := buildinfo.Resolve(buildinfo.Settings{
		Version: version,
		Commit:  commit,
		Date:    date,
	})
	code, err := cmd.Execute(info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
	os.Exit(code)
}
