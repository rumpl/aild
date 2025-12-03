// Package main provides the entry point for the AILD CLI.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
