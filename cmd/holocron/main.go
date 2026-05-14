package main

import (
	"os"

	"github.com/mattwynne/holocron/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	app := cli.New()
	os.Exit(app.Run(os.Args[1:]))
}
