package main

import (
	"io"
	"os"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/root"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func runCLI(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return root.Run(args, stdin, stdout, stderr, getenv)
}
