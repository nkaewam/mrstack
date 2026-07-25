package main

import (
	"os"

	"github.com/nkaewam/mrstack/internal/app"
	"github.com/nkaewam/mrstack/internal/cli"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	cli.Version = version
	os.Exit(cli.RunWithHandler(os.Args[1:], os.Stdout, os.Stderr, app.New()))
}
