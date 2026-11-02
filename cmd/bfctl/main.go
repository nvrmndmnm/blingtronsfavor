// Command bfctl is the Blingtron's Favor command-line addon manager.
package main

import (
	"os"

	"github.com/nvrmndmnm/blingtronsfavor/internal/cli"
)

// Build metadata, injected at link time via -ldflags.
var (
	version = "0.1.0-dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
	}))
}
