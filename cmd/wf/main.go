// Command wf starts and supervises Claude Code sessions.
package main

import (
	"fmt"
	"os"

	"github.com/jschell12/workforce/internal/cli"
	"github.com/jschell12/workforce/internal/paths"
)

// version is set at build time: -ldflags "-X main.version=$(git describe --tags --always)".
var version = "dev"

func main() {
	env := &cli.Env{Paths: paths.Resolve(), Out: os.Stdout, Err: os.Stderr}
	if err := cli.New(env, version).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wf:", err)
		os.Exit(1)
	}
}
