package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	name := flag.String("name", "world", "name to greet")
	showVersion := flag.Bool("version", false, "print CLI version")

	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments:", flag.Args())
		os.Exit(1)
	}

	fmt.Printf("Hello, %s!\n", *name)
}
