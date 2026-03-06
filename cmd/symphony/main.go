package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "symphony: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// TODO: CLI wiring (Phase 10)
	return nil
}
