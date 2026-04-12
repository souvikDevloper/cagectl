package container

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StateDir is the base directory where container state is persisted.
// Each container gets a subdirectory named by its ID.
const StateDir = "/var/lib/cagectl/containers"

// RuntimeDir holds transient runtime data (PIDs, sockets).
const RuntimeDir = "/var/run/cagectl"

// EnsureDirectories creates the required state and runtime directories.
func EnsureDirectories() error {
	dirs := []string{
		StateDir,
		RuntimeDir,
		filepath.Join(RuntimeDir, "overlays"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// SaveState persists the container state to disk as JSON.
// State file: /var/lib/cagectl/containers/<id>/state.json
func SaveState(state *ContainerState) error {
	dir := filepath.Join(StateDir, state.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state dir: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}
	return nil
}

// LoadState reads the container state from disk.
func LoadState(id string) (*ContainerState, error) {
	path := filepath.Join(StateDir, id, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read state for container %s: %w", id, err)
	}

	var state ContainerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}
	return &state, nil
}

// ListStates returns all persisted container states.
func ListStates() ([]*ContainerState, error) {
	entries, err := os.ReadDir(StateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list state dir: %w", err)
	}

	var states []*ContainerState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := LoadState(entry.Name())
		if err != nil {
			// Skip corrupted state files, log a warning
			fmt.Fprintf(os.Stderr, "warning: skipping container %s: %v\n", entry.Name(), err)
			continue
		}
		states = append(states, state)
	}
	return states, nil
}

// RemoveState deletes all persisted state for a container.
func RemoveState(id string) error {
	dir := filepath.Join(StateDir, id)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to remove state for container %s: %w", id, err)
	}
	return nil
}

// FindByName searches for a container by its human-readable name.
// Returns nil if not found.
func FindByName(name string) (*ContainerState, error) {
	states, err := ListStates()
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, nil
}

// FindByIDOrName tries to find a container by ID first, then by name.
// This allows users to reference containers by either identifier.
func FindByIDOrName(ref string) (*ContainerState, error) {
	// Try exact ID match first
	state, err := LoadState(ref)
	if err == nil {
		return state, nil
	}

	// Try prefix match on ID (like Docker's short IDs)
	states, err := ListStates()
	if err != nil {
		return nil, err
	}

	var matches []*ContainerState
	for _, s := range states {
		if len(ref) >= 4 && len(s.ID) >= len(ref) && s.ID[:len(ref)] == ref {
			matches = append(matches, s)
		}
		if s.Name == ref {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no container found with ID or name %q", ref)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous reference %q matches %d containers", ref, len(matches))
	}
}

// IsRunning checks if the container's init process is still alive.
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Sending signal 0 doesn't actually send a signal, but checks if the
	// process exists and we have permission to signal it.
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(os.Signal(nil))
	return err == nil
}
