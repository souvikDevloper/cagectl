// Package namespace configures Linux namespaces for container isolation.
//
// Linux namespaces are the fundamental building block of containers. Each namespace
// type isolates a different system resource, giving the container the illusion of
// having its own private instance of that resource.
//
// We use the following namespaces:
//   - PID:  Process IDs — container sees itself as PID 1
//   - MNT:  Mount points — container has its own filesystem tree
//   - UTS:  Hostname — container can set its own hostname
//   - IPC:  Inter-process communication — isolated shared memory/semaphores
//   - NET:  Networking — container gets its own network stack
//   - USER: User IDs — (optional) maps UIDs inside container to unprivileged UIDs outside
//
// The key insight: when we clone() a process with these namespace flags, the child
// process enters NEW namespaces. From inside the container, PID 1 is the container's
// init process. From outside, it's just a regular process with a normal host PID.
package namespace

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// CloneFlags returns the syscall flags needed to create all isolation namespaces.
// These flags are passed to clone() when spawning the container's init process.
//
// Each flag creates a NEW namespace of that type for the child process:
//   - CLONE_NEWPID: New PID namespace (container process becomes PID 1)
//   - CLONE_NEWNS:  New mount namespace (mount changes don't affect host)
//   - CLONE_NEWUTS: New UTS namespace (container can have its own hostname)
//   - CLONE_NEWIPC: New IPC namespace (isolated System V IPC / POSIX message queues)
//   - CLONE_NEWNET: New network namespace (empty network stack, needs veth setup)
func CloneFlags() uintptr {
	return syscall.CLONE_NEWPID |
		syscall.CLONE_NEWNS |
		syscall.CLONE_NEWUTS |
		syscall.CLONE_NEWIPC |
		syscall.CLONE_NEWNET
}

// SetupContainerCmd creates an exec.Cmd configured to run in new namespaces.
// This is the "outer" side — we re-exec ourselves with a special "init" argument
// so the child process can perform the "inner" namespace setup (pivot_root, etc).
//
// Why re-exec? Because namespace setup must happen INSIDE the new namespaces.
// The parent (host) process calls clone() with namespace flags, then the child
// needs to set up its mount tree, hostname, etc. from within those namespaces.
func SetupContainerCmd(args []string) *exec.Cmd {
	// Re-invoke ourselves with the "init" subcommand.
	// The child process will detect "init" and run container-internal setup.
	cmd := exec.Command("/proc/self/exe", append([]string{"init"}, args...)...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: CloneFlags(),

		// Unshare the mount namespace BEFORE the child runs.
		// This ensures mount propagation from host doesn't leak into the container.
		// MS_PRIVATE stops mount events from propagating between namespaces.
		Unshareflags: syscall.CLONE_NEWNS,
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd
}

// SetHostname sets the container's hostname within the UTS namespace.
// This only affects the container because we're in a new UTS namespace.
func SetHostname(hostname string) error {
	if err := syscall.Sethostname([]byte(hostname)); err != nil {
		return fmt.Errorf("failed to set hostname to %q: %w", hostname, err)
	}
	return nil
}

// SetupMountNamespace prepares the mount namespace inside the container.
// This is called from WITHIN the new mount namespace (child process side).
//
// The key operations:
//  1. Make all mounts private (prevent propagation to/from host)
//  2. Mount /proc inside the container so tools like `ps` work correctly
//     (they read from /proc, and we need the PID-namespace-aware /proc)
//  3. Mount /dev/pts for pseudo-terminal support
//  4. Mount /sys for sysfs access (read-only)
func SetupMountNamespace(rootfs string) error {
	// Step 1: Make all existing mounts private.
	// Without this, mount events could propagate between host and container.
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to make mounts private: %w", err)
	}

	// Step 2: Mount proc filesystem.
	// This gives us a PID-namespace-aware view of processes.
	// Inside the container, `ps aux` will only show container processes.
	procPath := rootfs + "/proc"
	if err := os.MkdirAll(procPath, 0755); err != nil {
		return fmt.Errorf("failed to create /proc: %w", err)
	}
	if err := syscall.Mount("proc", procPath, "proc", 0, ""); err != nil {
		return fmt.Errorf("failed to mount /proc: %w", err)
	}

	// Step 3: Mount a tmpfs at /dev for device nodes.
	devPath := rootfs + "/dev"
	if err := os.MkdirAll(devPath, 0755); err != nil {
		return fmt.Errorf("failed to create /dev: %w", err)
	}
	if err := syscall.Mount("tmpfs", devPath, "tmpfs", syscall.MS_NOSUID|syscall.MS_STRICTATIME, "mode=755,size=65536k"); err != nil {
		return fmt.Errorf("failed to mount /dev: %w", err)
	}

	// Create essential device nodes in /dev
	if err := createDeviceNodes(devPath); err != nil {
		return fmt.Errorf("failed to create device nodes: %w", err)
	}

	// Step 4: Mount devpts for pseudo-terminals.
	ptsPath := rootfs + "/dev/pts"
	if err := os.MkdirAll(ptsPath, 0755); err != nil {
		return fmt.Errorf("failed to create /dev/pts: %w", err)
	}
	if err := syscall.Mount("devpts", ptsPath, "devpts", 0, "newinstance,ptmxmode=0666"); err != nil {
		// Non-fatal: some minimal systems don't need pts
		fmt.Fprintf(os.Stderr, "warning: failed to mount /dev/pts: %v\n", err)
	}

	// Step 5: Mount sysfs (read-only).
	sysPath := rootfs + "/sys"
	if err := os.MkdirAll(sysPath, 0755); err != nil {
		return fmt.Errorf("failed to create /sys: %w", err)
	}
	if err := syscall.Mount("sysfs", sysPath, "sysfs", syscall.MS_RDONLY, ""); err != nil {
		// Non-fatal: container can work without sysfs
		fmt.Fprintf(os.Stderr, "warning: failed to mount /sys: %v\n", err)
	}

	return nil
}

// createDeviceNodes creates the minimal set of device nodes needed inside /dev.
// Containers need at least null, zero, random, urandom, and tty to function.
func createDeviceNodes(devPath string) error {
	devices := []struct {
		name  string
		mode  uint32
		major uint32
		minor uint32
	}{
		{"null", syscall.S_IFCHR | 0666, 1, 3},
		{"zero", syscall.S_IFCHR | 0666, 1, 5},
		{"random", syscall.S_IFCHR | 0666, 1, 8},
		{"urandom", syscall.S_IFCHR | 0666, 1, 9},
		{"tty", syscall.S_IFCHR | 0666, 5, 0},
		{"full", syscall.S_IFCHR | 0666, 1, 7},
	}

	for _, dev := range devices {
		path := devPath + "/" + dev.name
		// mkdev encodes major/minor into a single device number.
		devNum := int((dev.major << 8) | dev.minor)
		if err := syscall.Mknod(path, dev.mode, devNum); err != nil {
			return fmt.Errorf("failed to create device %s: %w", dev.name, err)
		}
	}

	// Create /dev/fd, /dev/stdin, /dev/stdout, /dev/stderr symlinks
	symlinks := []struct {
		old, new string
	}{
		{"/proc/self/fd", devPath + "/fd"},
		{"/proc/self/fd/0", devPath + "/stdin"},
		{"/proc/self/fd/1", devPath + "/stdout"},
		{"/proc/self/fd/2", devPath + "/stderr"},
	}
	for _, sl := range symlinks {
		if err := os.Symlink(sl.old, sl.new); err != nil {
			return fmt.Errorf("failed to create symlink %s -> %s: %w", sl.new, sl.old, err)
		}
	}

	return nil
}

// PivotRoot changes the root filesystem from the host's to the container's.
//
// This is the magic that makes the container see only its own filesystem.
// pivot_root() swaps the root mount:
//   - new root = our prepared rootfs (with overlay)
//   - old root = moved to a temporary directory, then unmounted
//
// After pivot_root, the container cannot see ANY host files.
// This is more secure than chroot because chroot can be escaped.
func PivotRoot(newRoot string) error {
	// pivot_root requires the new root and old root to be on different
	// mount points. Bind-mounting newRoot onto itself satisfies this.
	if err := syscall.Mount(newRoot, newRoot, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to bind mount new root: %w", err)
	}

	// Create a directory to hold the old root temporarily.
	oldRoot := newRoot + "/.pivot_root"
	if err := os.MkdirAll(oldRoot, 0700); err != nil {
		return fmt.Errorf("failed to create pivot dir: %w", err)
	}

	// pivot_root(new_root, put_old):
	//   - Changes the root mount to new_root
	//   - Moves the old root mount to put_old
	if err := syscall.PivotRoot(newRoot, oldRoot); err != nil {
		return fmt.Errorf("pivot_root failed: %w", err)
	}

	// After pivot, we're now inside the new root.
	// Change to / so we don't hold a reference to old root.
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("failed to chdir to /: %w", err)
	}

	// Unmount the old root (which is now at /.pivot_root).
	// MNT_DETACH lazily unmounts — the mount is removed from the namespace
	// immediately but cleanup happens when all references are dropped.
	if err := syscall.Unmount("/.pivot_root", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("failed to unmount old root: %w", err)
	}

	// Remove the now-empty old root directory.
	if err := os.RemoveAll("/.pivot_root"); err != nil {
		// Non-fatal, just a leftover empty dir
		fmt.Fprintf(os.Stderr, "warning: could not remove /.pivot_root: %v\n", err)
	}

	return nil
}
