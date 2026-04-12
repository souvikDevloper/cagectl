// init.go contains the container's init process logic.
//
// When we create a container, we re-exec ourselves with the "init" subcommand.
// This file handles that "init" codepath — it runs INSIDE the new namespaces
// and performs the container-internal setup:
//
//  1. Parse the rootfs path and hostname from arguments
//  2. Set up mounts inside the mount namespace (proc, dev, sys)
//  3. Pivot the root filesystem to the container's rootfs
//  4. Set the hostname in the UTS namespace
//  5. exec() the user's command (replacing this process)
//
// After exec(), the user's command becomes PID 1 inside the container.
// This is exactly how runc (the OCI runtime used by Docker) works.
package container

import (
	"fmt"
	"os"
	"syscall"

	"github.com/souvikinator/cagectl/internal/namespace"
)

// RunInit is called when the binary is re-exec'd with the "init" subcommand.
// At this point we are INSIDE the new namespaces but haven't set up the
// container's filesystem yet.
//
// Arguments format: init --rootfs <path> --hostname <name> --id <id> -- <command...>
func RunInit(args []string) error {
	var rootfs, hostname string
	var command []string

	// Parse arguments
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--rootfs":
			if i+1 < len(args) {
				rootfs = args[i+1]
				i += 2
			}
		case "--hostname":
			if i+1 < len(args) {
				hostname = args[i+1]
				i += 2
			}
		case "--id":
			// Skip the ID argument (used for logging)
			i += 2
		case "--":
			// Everything after -- is the command
			if i+1 < len(args) {
				command = args[i+1:]
			}
			i = len(args) // break the loop
		default:
			i++
		}
	}

	if rootfs == "" {
		return fmt.Errorf("init: --rootfs is required")
	}
	if len(command) == 0 {
		return fmt.Errorf("init: no command specified after --")
	}

	// Step 1: Set up mount namespace (proc, dev, sys inside rootfs)
	if err := namespace.SetupMountNamespace(rootfs); err != nil {
		return fmt.Errorf("init: mount namespace setup failed: %w", err)
	}

	// Step 2: Pivot root to the container's filesystem
	// After this, we can only see the container's files
	if err := namespace.PivotRoot(rootfs); err != nil {
		return fmt.Errorf("init: pivot_root failed: %w", err)
	}

	// Step 3: Set hostname in the UTS namespace
	if hostname != "" {
		if err := namespace.SetHostname(hostname); err != nil {
			return fmt.Errorf("init: hostname setup failed: %w", err)
		}
	}

	// Step 4: Find the command binary
	// Look for it in standard PATH locations inside the container
	binary, err := lookupBinary(command[0])
	if err != nil {
		return fmt.Errorf("init: command %q not found: %w", command[0], err)
	}

	// Step 5: exec() the user's command
	// This replaces the current process entirely — there's no return from exec.
	// The user's command becomes PID 1 inside the container.
	// We use syscall.Exec (not os/exec) because we want to REPLACE this process,
	// not spawn a child. PID 1 must be the user's process for signal handling
	// to work correctly.
	return syscall.Exec(binary, command, os.Environ())
}

// lookupBinary searches for a binary in the container's filesystem.
// It checks if the path is absolute first, then searches standard directories.
func lookupBinary(name string) (string, error) {
	// If it's an absolute path, use it directly
	if name[0] == '/' {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
		return "", fmt.Errorf("binary not found at %s", name)
	}

	// Search in standard PATH directories
	searchPaths := []string{
		"/usr/local/sbin",
		"/usr/local/bin",
		"/usr/sbin",
		"/usr/bin",
		"/sbin",
		"/bin",
	}

	for _, dir := range searchPaths {
		path := dir + "/" + name
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("binary %q not found in container PATH", name)
}
