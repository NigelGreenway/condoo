// Command condoo: Context + Todo. Agents put their questions and requests in it; the Herdr
// Condoo panel shows them. `condoo --help` for the commands.
package main

import (
	"os"

	"github.com/NigelGreenway/condoo/internal/cli"
)

// version is set at release time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Default(version)))
}
