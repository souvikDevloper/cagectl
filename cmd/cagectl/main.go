// cagectl — A lightweight container runtime built from scratch.
//
// This is the main entry point for the cagectl binary. The binary serves
// two purposes:
//
//  1. CLI tool: When invoked normally (cagectl run, cagectl list, etc.),
//     it processes user commands through the cobra CLI framework.
//
//  2. Container init: When re-invoked via /proc/self/exe with the "init"
//     subcommand, it runs inside the new namespaces to perform container
//     setup (pivot_root, mount proc, etc.) before exec()'ing the user's command.
//
// This dual-purpose design is the same pattern used by runc (Docker's
// low-level container runtime). The key insight is that namespace setup
// must happen INSIDE the new namespaces, so we need a process that runs
// there — and the simplest approach is to re-exec ourselves.
package main

import "github.com/souvikDevloper/cagectl/internal/cli"

func main() {
	cli.Execute()
}
