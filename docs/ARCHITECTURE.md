# Architecture Deep Dive

This document explains how cagectl works internally, walking through every kernel mechanism used to create a Linux container from scratch.

## Table of Contents

1. [The Big Picture](#the-big-picture)
2. [Linux Namespaces](#linux-namespaces)
3. [The Re-exec Pattern](#the-re-exec-pattern)
4. [Mount Namespace & pivot_root](#mount-namespace--pivot_root)
5. [Cgroups v2](#cgroups-v2)
6. [OverlayFS](#overlayfs)
7. [Container Networking](#container-networking)
8. [State Management](#state-management)
9. [Container Lifecycle](#container-lifecycle)

---

## The Big Picture

A "container" is not a real thing in the Linux kernel. There is no `container` syscall. Instead, a container is the emergent result of combining several independent kernel features:

```
Container = Namespaces + Cgroups + Filesystem isolation + Networking
```

Each feature solves one piece of the puzzle:

| Feature | What it isolates | Kernel mechanism |
|---------|-----------------|------------------|
| PID namespace | Process tree | `clone(CLONE_NEWPID)` |
| Mount namespace | Filesystem mounts | `clone(CLONE_NEWNS)` |
| UTS namespace | Hostname | `clone(CLONE_NEWUTS)` |
| IPC namespace | Shared memory, semaphores | `clone(CLONE_NEWIPC)` |
| Network namespace | Network stack | `clone(CLONE_NEWNET)` |
| cgroups v2 | CPU, memory, PIDs | `/sys/fs/cgroup/` |
| OverlayFS | Filesystem layers | `mount -t overlay` |
| veth pairs | Network connectivity | `ip link add type veth` |

cagectl combines all of these into a coherent container runtime.

---

## Linux Namespaces

Namespaces are the foundation of container isolation. Each namespace type creates a new, isolated instance of a global system resource.

### PID Namespace

```
HOST VIEW:                    CONTAINER VIEW:
PID 1: systemd               PID 1: /bin/sh  ← (actually PID 42987 on host)
PID 2: kthreadd              PID 2: /bin/ls
...
PID 42987: /bin/sh
PID 42988: /bin/ls
```

Inside the PID namespace, the container's init process becomes PID 1. It cannot see any host processes. The host can still see the container's processes (with their real PIDs).

**Why PID 1 matters**: In Linux, PID 1 has special responsibilities — it adopts orphaned child processes and receives SIGCHLD for any child that exits. This is why long-running containers typically need an init system or a process that handles signals properly.

### Network Namespace

A new network namespace starts completely empty — no interfaces, no routes, no firewall rules. Not even a loopback interface. We have to set everything up:

```go
// These clone flags create all namespaces at once
syscall.CLONE_NEWPID |   // New PID namespace
syscall.CLONE_NEWNS  |   // New mount namespace
syscall.CLONE_NEWUTS |   // New UTS namespace (hostname)
syscall.CLONE_NEWIPC |   // New IPC namespace
syscall.CLONE_NEWNET     // New network namespace
```

---

## The Re-exec Pattern

This is the trickiest part of building a container runtime. The problem:

1. **Parent process** (on the host) needs to call `clone()` with namespace flags
2. **Child process** (inside new namespaces) needs to set up mounts, pivot_root, etc.
3. These setup steps MUST happen inside the new namespaces — the parent can't do them

The solution: **re-exec ourselves**.

```
cagectl run --rootfs ./rootfs -- /bin/sh
    │
    ├── Parent: clone() with CLONE_NEW* flags
    │      creates child in new namespaces
    │
    └── Child: exec(/proc/self/exe init --rootfs ... -- /bin/sh)
               │
               ├── Setup mounts (/proc, /dev, /sys)
               ├── pivot_root() to container rootfs
               ├── Set hostname
               └── exec(/bin/sh)  ← becomes PID 1
```

`/proc/self/exe` is a symlink to the currently running binary. By exec'ing ourselves with the `init` subcommand, the child process runs our init code inside the new namespaces. After setup, it calls `exec()` on the user's command, which replaces the init process entirely.

This is the exact same pattern used by **runc** (Docker's runtime).

---

## Mount Namespace & pivot_root

### Why pivot_root, not chroot?

`chroot` merely changes the root directory for the current process. It can be escaped:

```c
// Classic chroot escape
mkdir("escape");
chroot("escape");
// Repeat .. enough times and you're back at the real root
chdir("../../../../..");
chroot(".");
```

`pivot_root` is different — it changes the **root mount** for the entire mount namespace. The old root is explicitly unmounted and becomes inaccessible:

```go
// 1. Bind mount the new root (required by pivot_root)
syscall.Mount(newRoot, newRoot, "", MS_BIND|MS_REC, "")

// 2. Create a place to put the old root
os.MkdirAll(newRoot + "/.pivot_root", 0700)

// 3. Swap the root mount
syscall.PivotRoot(newRoot, newRoot + "/.pivot_root")

// 4. Unmount and remove the old root
syscall.Unmount("/.pivot_root", MNT_DETACH)
os.RemoveAll("/.pivot_root")
```

After step 4, there is no way to access the host filesystem from inside the container.

### Mount Setup

Before pivoting, we mount essential filesystems inside the container:

```
/proc   — Process information filesystem (PID-namespace aware)
/dev    — Device nodes (null, zero, random, urandom, tty)
/dev/pts— Pseudo-terminal devices
/sys    — Sysfs (read-only)
```

The `/proc` mount is especially important: because we're in a new PID namespace, `/proc` will only show the container's processes. `ps aux` inside the container will only see container processes.

---

## Cgroups v2

### Unified Hierarchy

cgroup v2 uses a single filesystem tree at `/sys/fs/cgroup/`. Each container gets its own directory:

```
/sys/fs/cgroup/
└── cagectl/                         ← Our parent cgroup
    └── a1b2c3d4-.../                ← Container's cgroup
        ├── cgroup.procs             ← PIDs in this cgroup
        ├── memory.max               ← Hard memory limit (bytes)
        ├── memory.current           ← Current usage (bytes)
        ├── memory.swap.max          ← Swap limit (we set to 0)
        ├── cpu.max                  ← "quota period" (microseconds)
        ├── cpu.stat                 ← CPU usage statistics
        ├── pids.max                 ← Maximum process count
        └── pids.current             ← Current process count
```

### Memory Limits

```go
// Set hard memory limit to 128MB
os.WriteFile("/sys/fs/cgroup/cagectl/<id>/memory.max", []byte("134217728"), 0644)

// Disable swap (for predictable performance)
os.WriteFile("/sys/fs/cgroup/cagectl/<id>/memory.swap.max", []byte("0"), 0644)
```

When a container exceeds `memory.max`, the kernel's OOM killer terminates processes inside the cgroup. This is a **hard limit** — the kernel guarantees it won't be exceeded.

### CPU Bandwidth Control

```go
// Allow 50ms of CPU time every 100ms (= 50% of one core)
os.WriteFile("/sys/fs/cgroup/cagectl/<id>/cpu.max", []byte("50000 100000"), 0644)
```

The CFS (Completely Fair Scheduler) bandwidth controller works with a **quota/period** model:
- **Period**: The scheduling window (default 100ms)
- **Quota**: How much CPU time the cgroup can use per period

A quota of 200000 with period 100000 means 2 full CPU cores.

### Fork Bomb Protection

```go
// Maximum 64 processes (including threads)
os.WriteFile("/sys/fs/cgroup/cagectl/<id>/pids.max", []byte("64"), 0644)
```

Without this, a `:(){ :|:& };:` fork bomb inside a container would exhaust the entire host's PID space, affecting ALL processes on the system.

---

## OverlayFS

### Layer Model

```
┌──────────────────────────────────────┐
│          Merged View                 │  ← What the container sees
│     (mount point: .../merged/)       │
├──────────────────────────────────────┤
│  Upper Layer (writable)              │  ← Container's changes
│     (.../upper/)                     │
├──────────────────────────────────────┤
│  Lower Layer (read-only)             │  ← Alpine Linux rootfs
│     (./rootfs/)                      │
└──────────────────────────────────────┘
```

### How Reads Work

When a process reads a file, OverlayFS checks the upper layer first. If the file exists there (because the container modified it), that version is returned. Otherwise, it falls through to the lower layer (the base image).

### How Writes Work (Copy-on-Write)

When a container writes to a file that only exists in the lower layer:

1. OverlayFS copies the file from the lower layer to the upper layer (copy-up)
2. The write is applied to the upper layer copy
3. The lower layer is never modified

This means:
- Multiple containers can share the same base rootfs
- Each container's changes are isolated in its own upper layer
- Cleanup is trivial: delete the upper layer

### How Deletes Work (Whiteout Files)

When a container deletes a file from the lower layer, OverlayFS creates a **whiteout file** in the upper layer. This is a special character device that tells OverlayFS to hide the lower layer file.

```go
// Mount overlay
mount("overlay", mergedDir, "overlay", 0,
    "lowerdir=/rootfs,upperdir=/upper,workdir=/work")
```

---

## Container Networking

### The Problem

A new network namespace is completely empty — no interfaces, no routes. The container can't communicate with anything.

### The Solution: veth + Bridge

```
HOST                              CONTAINER
┌──────────┐    ┌─────────┐    ┌──────────┐
│  eth0    │───▶│  cage0  │───▶│  eth0    │
│(external)│    │(bridge) │    │(10.10.0.2)
│          │    │10.10.0.1│    │          │
└──────────┘    └─────────┘    └──────────┘
                     │
                ┌────┴────┐
                │iptables │
                │  NAT    │
                │MASQUERADE│
                └─────────┘
```

### Step-by-Step Network Setup

**1. Create a bridge** (like a virtual network switch):
```go
bridge := &netlink.Bridge{LinkAttrs: netlink.NewLinkAttrs()}
bridge.Name = "cage0"
netlink.LinkAdd(bridge)
netlink.AddrAdd(bridge, "10.10.0.1/24")
netlink.LinkSetUp(bridge)
```

**2. Create a veth pair** (two virtual NICs connected by a pipe):
```go
veth := &netlink.Veth{
    LinkAttrs: netlink.LinkAttrs{Name: "vethXXXX"},
    PeerName:  "eth0",
}
netlink.LinkAdd(veth)
```

**3. Attach host side to bridge**:
```go
netlink.LinkSetMaster(hostVeth, bridge)
netlink.LinkSetUp(hostVeth)
```

**4. Move container side into the container's network namespace**:
```go
netlink.LinkSetNsPid(containerVeth, containerPID)
```

**5. Configure inside the container namespace** (using `netns.Set()`):
```go
netlink.AddrAdd(containerVeth, "10.10.0.2/24")
netlink.LinkSetUp(containerVeth)
netlink.RouteAdd(&netlink.Route{Gw: "10.10.0.1"})
```

**6. Enable NAT** (so containers can reach the internet):
```bash
echo 1 > /proc/sys/net/ipv4/ip_forward
iptables -t nat -A POSTROUTING -s 10.10.0.0/24 ! -o cage0 -j MASQUERADE
```

### Why runtime.LockOSThread()?

Network namespaces are per-thread in Linux, not per-process. Go's goroutine scheduler can move goroutines between OS threads at any time. If we switch to the container's network namespace and Go moves another goroutine onto our thread, that goroutine would accidentally run in the container's namespace.

`runtime.LockOSThread()` pins the goroutine to its current OS thread for the duration of the namespace operation.

---

## State Management

Container state is persisted as JSON files:

```
/var/lib/cagectl/containers/
└── <container-id>/
    └── state.json
```

This allows `cagectl list` and `cagectl inspect` to work across CLI invocations. The state includes the container's config, PID, status, timestamps, and filesystem paths.

State transitions follow the OCI runtime spec:

```
creating ──▶ created ──▶ running ──▶ stopped
                                        │
                                        ▼
                                    (removed)
```

---

## Container Lifecycle

### Create

1. Generate UUID and name
2. Validate configuration
3. Create overlay filesystem (lower + upper + work + merged)
4. Copy `/etc/resolv.conf` into container rootfs
5. Create cgroup directory
6. Set resource limits (memory, CPU, PIDs)
7. Persist state to disk

### Start

1. Build init process arguments
2. `clone()` with namespace flags (via `exec.Command` + `SysProcAttr`)
3. Add child PID to cgroup
4. Set up networking (bridge, veth, IP, routes, NAT)
5. Wait for container process to exit
6. Update state with exit code

### Stop

1. Send `SIGTERM` to container's init process
2. Wait up to 10 seconds for graceful shutdown
3. If still alive, send `SIGKILL`
4. Update state to "stopped"

### Remove

1. Stop if still running
2. Remove cgroup directory
3. Unmount and delete overlay filesystem
4. Delete host-side veth interface
5. Remove state files from disk

---

## Security Considerations

cagectl is an **educational project** and is NOT suitable for production use. It lacks several security features that production runtimes implement:

- **No user namespaces**: Runs as root inside and outside the container
- **No seccomp**: All syscalls are available inside the container
- **No AppArmor/SELinux**: No mandatory access control profiles
- **No capability dropping**: Container runs with all capabilities
- **No read-only rootfs option**: The merged overlay is writable

For production container security, see how runc implements these features in the [OCI runtime spec](https://github.com/opencontainers/runtime-spec).
