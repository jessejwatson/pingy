// Command pingy pings many hosts at once, in the background, and graphs
// them.
package main

import (
	"os"

	"pingy/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.NewApp(version).Run(os.Args[1:]))
}
