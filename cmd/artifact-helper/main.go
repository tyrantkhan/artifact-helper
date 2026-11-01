package main

import (
	"os"

	"github.com/tyrantkhan/claude-artifact-helper/internal/cli"
)

func main() {
	os.Exit(cli.Main(&cli.Env{
		Args:    os.Args[1:],
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Getenv:  os.Getenv,
		Environ: os.Environ,
	}))
}
