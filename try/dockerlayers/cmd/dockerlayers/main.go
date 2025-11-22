package main

import (
	"fmt"
	"os"

	"lang/try/dockerlayers"
)

func main() {
	if err := dockerlayers.RunCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "dockerlayers: %v\n", err)
		os.Exit(1)
	}
}
