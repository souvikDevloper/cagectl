// Package filesystem manages the OverlayFS setup for container rootfs.
//
// OverlayFS is a union filesystem that combines multiple directories (layers)
// into a single coherent filesystem view. This is the same technology Docker
// uses for its image layers.
//
// How OverlayFS works:
//
//	┌──────────────────────────────────────┐
//	│          Merged View (mount point)    │  ← What the container sees
//	├──────────────────────────────────────┤
//	│  Upper Layer (writable)              │  ← Container's changes go here
//	├──────────────────────────────────────┤
//	│  Lower Layer (read-only)             │  ← Base rootfs (Alpine Linux)
//	└──────────────────────────────────────┘
//
// Key properties:
//   - Reads go to upper layer first; if file doesn't exist, falls through to lower
//   - Writes ALWAYS go to the upper layer (copy-on-write)
//   - Deletes create a "whiteout" file in upper layer (lower layer unchanged)
//   - Lower layer is NEVER modified — multiple containers can share the same base
//
// This gives us:
//   - Efficient disk usage: containers share the base image
//   - Isolation: each container's changes are in its own upper layer
//   - Easy cleanup: delete the upper layer to reset to base image
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// OverlayDirs holds the paths for an OverlayFS mount.
type OverlayDirs struct {
	// LowerDir is the read-only base layer (the rootfs image).
	LowerDir string

	// UpperDir is the writable layer where container changes accumulate.
	UpperDir string

	// WorkDir is required by OverlayFS for atomic copy-up operations.
	// Must be on the same filesystem as UpperDir.
	WorkDir string

	// MergedDir is the mount point where the unified view appears.
	// This becomes the container's root filesystem.
	MergedDir string
}

// Manager handles overlay filesystem setup and teardown.
type Manager struct {
	ContainerID string
	BaseDir     string // Base directory for overlay storage (e.g., /var/run/cagectl/overlays)
	Dirs        OverlayDirs
}

// NewManager creates a new filesystem manager for the given container.
func NewManager(containerID, rootfsPath string) *Manager {
	baseDir := filepath.Join("/var/run/cagectl/overlays", containerID)

	return &Manager{
		ContainerID: containerID,
		BaseDir:     baseDir,
		Dirs: OverlayDirs{
			LowerDir:  rootfsPath,
			UpperDir:  filepath.Join(baseDir, "upper"),
			WorkDir:   filepath.Join(baseDir, "work"),
			MergedDir: filepath.Join(baseDir, "merged"),
		},
	}
}

// Setup creates the overlay directories and mounts the OverlayFS.
//
// The mount syscall for overlayfs takes these options:
//
//	mount -t overlay overlay -o lowerdir=<lower>,upperdir=<upper>,workdir=<work> <merged>
//
// After this call, Dirs.MergedDir contains a unified view of the filesystem
// where reads fall through to the base rootfs and writes go to the upper layer.
func (m *Manager) Setup() error {
	// Verify the lower (base rootfs) directory exists
	if _, err := os.Stat(m.Dirs.LowerDir); os.IsNotExist(err) {
		return fmt.Errorf("rootfs not found at %s — run 'cagectl setup-rootfs' first", m.Dirs.LowerDir)
	}

	// Create the overlay directories
	for _, dir := range []string{m.Dirs.UpperDir, m.Dirs.WorkDir, m.Dirs.MergedDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create overlay directory %s: %w", dir, err)
		}
	}

	// Mount the overlay filesystem.
	// The mount options string tells the kernel which directories to use for each layer.
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s",
		m.Dirs.LowerDir, m.Dirs.UpperDir, m.Dirs.WorkDir)

	if err := syscall.Mount("overlay", m.Dirs.MergedDir, "overlay", 0, opts); err != nil {
		return fmt.Errorf("failed to mount overlayfs at %s: %w (opts: %s)", m.Dirs.MergedDir, err, opts)
	}

	return nil
}

// GetMergedPath returns the path to the merged overlay directory.
// This is what gets used as the container's rootfs for pivot_root.
func (m *Manager) GetMergedPath() string {
	return m.Dirs.MergedDir
}

// Cleanup unmounts the overlay filesystem and removes all overlay directories.
// This should be called when the container is removed.
func (m *Manager) Cleanup() error {
	// Unmount the overlay
	if err := syscall.Unmount(m.Dirs.MergedDir, syscall.MNT_DETACH); err != nil {
		// If already unmounted, that's fine
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: failed to unmount overlay at %s: %v\n", m.Dirs.MergedDir, err)
		}
	}

	// Remove all overlay directories
	if err := os.RemoveAll(m.BaseDir); err != nil {
		return fmt.Errorf("failed to clean up overlay dirs at %s: %w", m.BaseDir, err)
	}

	return nil
}

// GetLayerSize calculates the size of the upper (writable) layer.
// This tells you how much disk space the container's changes are using.
func (m *Manager) GetLayerSize() (int64, error) {
	var size int64
	err := filepath.Walk(m.Dirs.UpperDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// SetupBindMount is the fallback when OverlayFS is not available.
// It bind-mounts the rootfs directly (changes will persist in the rootfs).
func SetupBindMount(rootfsPath, mountPoint string) error {
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	flags := uintptr(syscall.MS_BIND | syscall.MS_REC)
	if err := syscall.Mount(rootfsPath, mountPoint, "", flags, ""); err != nil {
		return fmt.Errorf("failed to bind mount rootfs: %w", err)
	}

	return nil
}

// CopyResolveConf copies the host's DNS configuration into the container.
// Without this, the container can't resolve domain names.
func CopyResolveConf(mergedRoot string) error {
	src := "/etc/resolv.conf"
	dst := filepath.Join(mergedRoot, "etc", "resolv.conf")

	// Ensure the target directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("failed to create etc directory: %w", err)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		// If host doesn't have resolv.conf, create a basic one
		data = []byte("nameserver 8.8.8.8\nnameserver 8.8.4.4\n")
	}

	if err := os.WriteFile(dst, data, 0644); err != nil {
		return fmt.Errorf("failed to write resolv.conf: %w", err)
	}

	return nil
}
