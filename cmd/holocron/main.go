package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version)
			return
		case "help", "--help", "-h":
			usage()
			return
		}
	}

	usage()
}

func usage() {
	fmt.Fprintln(os.Stdout, `holocron - search and reopen your AI coding agent sessions

Usage:
  holocron search Q [--json]        Search indexed sessions
  holocron pick [Q]                 Pick and reopen a session in the TUI
  holocron open ID                  Reopen an indexed session
  holocron service COMMAND          Manage the background indexer
  holocron version                  Print version

Service commands:
  install, uninstall, start, stop, status`)
}
