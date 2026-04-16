package main

import (
	"fmt"
	"os"

	"github.com/colony-2/c2j/c2j/internal/cmd"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	code, err := cmd.Execute(Version, BuildTime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
	os.Exit(code)
}
