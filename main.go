package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "hmt: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("hmt - Claude Code token usage tracker")
	return nil
}
