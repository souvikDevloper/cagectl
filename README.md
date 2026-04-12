# cagectl

A lightweight container runtime built from scratch in Go — demonstrating how Linux containers actually work under the hood.

```
$ sudo cagectl run --rootfs ./rootfs --memory 128m --cpus 0.5 -- /bin/sh
Creating container...
Container brave-falcon (a1b2c3d4e5f6) created
Starting container...

cage:/# ps aux
PID   USER     COMMAND
    1 root     /bin/sh

cage:/# hostname
cage

cage:/# ip addr show eth0
3: eth0: <BROADCAST,MULTICAST,UP> mtu 1500
    inet 10.10.0.2/24 scope global eth0
```

**cagectl** creates fully isolated containers using the same Linux primitives that Docker and Kubernetes use at their core: **namespaces** for isolation, **cgroups v2** for resource limits, **OverlayFS** for layered filesystems, and **veth pairs** for networking. No Docker. No containerd. Just raw Linux syscalls.

---

## Why This Exists

Every developer uses containers, but few understand what happens when you type `docker run`. This project peels back the abstraction layers to expose the actual kernel mechanisms:

- **What does "isolation" really mean?** → Linux namespaces (`clone()` with `CLONE_NEWPID`, `CLONE_NEWNS`, etc.)
- **How are resources limited?** → cgroup v2 controllers (`memory.max`, `cpu.max`, `pids.max`)
- **How does the container filesystem work?** → OverlayFS with copy-on-write semantics
- **How does container networking work?** → veth pairs, bridges, NAT via iptables

See [ARCHITECTURE.md](docs/ARCHITECTURE.md) for a deep technical walkthrough.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    cagectl CLI                          │
│              (run, exec, list, stop, rm)                │
├─────────────────────────────────────────────────────────┤
│                  Container Runtime                       │
│          (lifecycle orchestration layer)                 │
├────────────┬────────────┬──────────────┬────────────────┤
│ Namespaces │  Cgroups   │  OverlayFS   │   Networking   │
│            │   v2       │              │                │
│ ┌────────┐ │ ┌────────┐ │ ┌──────────┐ │ ┌────────────┐ │
│ │  PID   │ │ │ memory │ │ │  lower   │ │ │   bridge   │ │
│ │  MNT   │ │ │  cpu   │ │ │  upper   │ │ │   veth     │ │
│ │  UTS   │ │ │  pids  │ │ │  merged  │ │ │   NAT      │ │
│ │  IPC   │ │ └────────┘ │ └──────────┘ │ │   routes   │ │
│ │  NET   │ │            │              │ └────────────┘ │
│ └────────┘ │            │              │                │
├────────────┴────────────┴──────────────┴────────────────┤
│                    Linux Kernel                          │
└─────────────────────────────────────────────────────────┘
```

### How `cagectl run` Works (Step by Step)

```
1. Parse config         ─── Validate rootfs, resource limits, networking options
       │
2. Create OverlayFS     ─── Mount overlay (lowerdir=rootfs, upperdir=writable layer)
       │
3. Setup cgroup v2      ─── Create /sys/fs/cgroup/cagectl/<id>/, set limits
       │
4. Clone with NS flags  ─── clone(CLONE_NEWPID | CLONE_NEWNS | CLONE_NEWUTS | CLONE_NEWIPC | CLONE_NEWNET)
       │
5. [Child] Re-exec init ─── /proc/self/exe init --rootfs <merged> --hostname cage -- /bin/sh
       │
6. [Child] Setup mounts ─── Mount /proc, /dev, /sys inside new mount namespace
       │
7. [Child] pivot_root   ─── Swap root filesystem, unmount old root
       │
8. [Parent] Add to cgroup── Write child PID to cgroup.procs
       │
9. [Parent] Setup veth   ── Create veth pair, attach to bridge, move peer into container NS
       │
10. [Child] exec()      ─── Replace init process with user command (becomes PID 1)
```

---

## Features

### Process Isolation (Linux Namespaces)
- **PID namespace**: Container sees itself as PID 1. Host processes invisible.
- **Mount namespace**: Isolated filesystem tree. Changes don't affect host.
- **UTS namespace**: Container has its own hostname.
- **IPC namespace**: Isolated System V IPC and POSIX message queues.
- **Network namespace**: Own network stack, IP address, routing table.

### Resource Limits (cgroups v2)
- **Memory**: Hard limit with OOM kill. Swap disabled for predictability.
- **CPU**: Bandwidth control via CFS scheduler (quota/period model).
- **PIDs**: Process count limit (fork bomb protection).
- Live stats via `cagectl inspect`.

### Layered Filesystem (OverlayFS)
- **Copy-on-write**: Base image shared across containers (read-only lower layer).
- **Writable upper layer**: Each container's changes are isolated.
- **Efficient cleanup**: Delete upper layer to reset to base image.

### Container Networking
- **veth pairs**: Virtual ethernet pipe connecting container to host.
- **Bridge network** (`cage0`): Containers can talk to each other and host.
- **NAT via iptables**: Containers can access the internet through host.
- **Automatic IP allocation**: Each container gets a unique 10.10.0.x address.

---

## Quick Start

### Prerequisites
- Linux kernel 4.18+ (for cgroup v2 and OverlayFS)
- Go 1.22+
- Root privileges (container operations require CAP_SYS_ADMIN)

### Build

```bash
git clone https://github.com/souvikDevloper/cagectl.git
cd cagectl
make build
```

### Download a Root Filesystem

```bash
# Downloads Alpine Linux minimal rootfs (~3MB)
sudo bash scripts/setup-rootfs.sh ./rootfs
```

### Run Your First Container

```bash
# Interactive shell
sudo ./bin/cagectl run --rootfs ./rootfs -- /bin/sh

# You're now inside the container!
# Try: ps aux, hostname, ls /, ip addr show
```

### Resource Limits

```bash
# 128MB memory, half a CPU core, max 32 processes
sudo ./bin/cagectl run \
  --rootfs ./rootfs \
  --memory 128m \
  --cpus 0.5 \
  --pids 32 \
  --hostname sandbox \
  -- /bin/sh
```

### Container Management

```bash
# List running containers
sudo ./bin/cagectl list

# List all containers (including stopped)
sudo ./bin/cagectl list --all

# Inspect container details + live resource usage
sudo ./bin/cagectl inspect <container-id-or-name>

# Execute command in running container
sudo ./bin/cagectl exec <container-id> -- /bin/ls -la /

# Stop a container (SIGTERM → 10s timeout → SIGKILL)
sudo ./bin/cagectl stop <container-id>

# Remove container and clean up all resources
sudo ./bin/cagectl remove <container-id>
```

---

## Project Structure

```
cagectl/
├── cmd/
│   └── cagectl/
│       └── main.go              # Entry point (CLI + re-exec init)
├── internal/
│   ├── cli/                     # Cobra CLI commands
│   │   ├── root.go              # Root command + version info
│   │   ├── run.go               # `cagectl run` — create + start container
│   │   ├── exec.go              # `cagectl exec` — run cmd in existing container
│   │   ├── list.go              # `cagectl list` — show containers
│   │   ├── stop.go              # `cagectl stop` — graceful shutdown
│   │   ├── remove.go            # `cagectl remove` — cleanup resources
│   │   ├── inspect.go           # `cagectl inspect` — detailed info + stats
│   │   └── init_cmd.go          # Hidden init command (container-internal)
│   ├── container/               # Core container lifecycle
│   │   ├── config.go            # Configuration types + validation
│   │   ├── state.go             # State persistence (JSON on disk)
│   │   ├── runtime.go           # Lifecycle orchestration (create/start/stop)
│   │   └── init.go              # Container init process (runs inside NS)
│   ├── namespace/               # Linux namespace setup
│   │   └── namespace.go         # Clone flags, mount setup, pivot_root
│   ├── cgroup/                  # cgroup v2 resource management
│   │   └── cgroup.go            # Memory, CPU, PIDs limits + stats
│   ├── filesystem/              # OverlayFS management
│   │   └── overlay.go           # Setup/teardown overlay layers
│   └── network/                 # Container networking
│       └── network.go           # Bridge, veth pairs, NAT, IP allocation
├── scripts/
│   └── setup-rootfs.sh          # Alpine Linux rootfs downloader
├── docs/
│   └── ARCHITECTURE.md          # Deep technical walkthrough
├── .github/
│   └── workflows/
│       └── ci.yml               # Build, lint, security scan
├── Makefile                     # Build, install, test targets
├── go.mod
├── go.sum
├── LICENSE                      # MIT
└── README.md
```

---

## How It Compares to Docker

| Feature | Docker | cagectl |
|---------|--------|---------|
| Namespaces (PID, MNT, UTS, IPC, NET) | ✅ | ✅ |
| cgroup resource limits | ✅ (v1 + v2) | ✅ (v2 only) |
| OverlayFS / layered filesystem | ✅ (multiple drivers) | ✅ (overlay2) |
| Container networking | ✅ (bridge, overlay, host) | ✅ (bridge + NAT) |
| Image pulling (Docker Hub) | ✅ | ❌ (manual rootfs) |
| Multi-host networking | ✅ | ❌ |
| Volumes | ✅ | ❌ |
| Docker Compose / orchestration | ✅ | ❌ |
| Production ready | ✅ | ❌ (educational) |

**cagectl implements the same core primitives** — the difference is Docker adds layers of abstraction (containerd, image management, networking plugins, orchestration) on top of these same building blocks.

---

## Key Design Decisions

### Why re-exec (`/proc/self/exe`) instead of fork?
Namespace setup must happen *inside* the new namespaces. The parent process calls `clone()` with namespace flags, but the child needs to run `pivot_root`, mount `/proc`, and set the hostname from within those namespaces. Re-executing ourselves with an `init` subcommand is the standard pattern (used by runc).

### Why cgroup v2 only?
cgroup v2 (unified hierarchy) is the modern interface. cgroup v1 is legacy and involves managing multiple hierarchies. All modern distros default to v2. Building for v2 only keeps the code clean and forward-looking.

### Why OverlayFS over bind mounts?
OverlayFS gives us copy-on-write semantics — multiple containers can share the same base image without duplicating it on disk. A bind mount would mean each container modifies the original rootfs.

### Why `pivot_root` instead of `chroot`?
`chroot` can be escaped (it's not a security boundary). `pivot_root` actually changes the root mount point, making the old root inaccessible. This is the correct way to set up a container's filesystem.

---

## Learning Resources

If you want to understand the internals deeper:

- **Linux Namespaces**: `man 7 namespaces`, `man 2 clone`
- **cgroups v2**: `man 7 cgroups`, [kernel docs](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
- **OverlayFS**: [kernel docs](https://www.kernel.org/doc/html/latest/filesystems/overlayfs.html)
- **pivot_root**: `man 2 pivot_root`
- **veth pairs**: `man 4 veth`
- **OCI Runtime Spec**: [opencontainers/runtime-spec](https://github.com/opencontainers/runtime-spec)
- **runc source**: [opencontainers/runc](https://github.com/opencontainers/runc) (Docker's runtime, same architecture)

---

## Contributing

Contributions welcome! Some ideas for extending cagectl:

- [ ] User namespace support (rootless containers)
- [ ] Seccomp filter (syscall allowlisting)
- [ ] Volume mounts (`--volume /host/path:/container/path`)
- [ ] Port mapping (`--publish 8080:80`)
- [ ] Container image pulling (from Docker Hub / OCI registries)
- [ ] Checkpoint/restore (CRIU integration)
- [ ] Resource usage monitoring daemon
- [ ] AppArmor / SELinux profiles

---

## License

MIT — see [LICENSE](LICENSE).

---

**Built to understand containers at the kernel level.** If this helped you learn something, consider giving it a ⭐.
