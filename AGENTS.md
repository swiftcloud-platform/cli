# SwiftCloud CLI - Agent Reference

## Project Overview

**SwiftCloud CLI** (`cloud`) is a Go-based command-line tool for managing SwiftCloud infrastructure on Proxmox VE clusters. It uses the **Cobra** framework for CLI structure and communicates with Proxmox nodes via **SSH** (remote mode) or **os/exec** (local mode).

### Purpose

Automate Proxmox VM template lifecycle, shared storage setup, and node initialization — replacing manual `qm` command workflows with structured, repeatable CLI operations.

### Key Design Decisions

- **Two execution modes**: Remote (SSH into Proxmox host) and Local (run `qm` commands directly on the host)
- **No Proxmox API dependency**: Uses `qm` CLI commands over SSH instead of the Proxmox REST API, because operations like `qm importdisk` have no API equivalent
- **CommandExecutor pattern**: Abstracts command execution behind an interface, allowing the same logic to run via SSH or locally
- **No database integration**: Template registration in SwiftCloud's PostgreSQL is handled separately; this CLI focuses on Proxmox-level operations

---

## Architecture

```
cli/
├── main.go                         # Entry point, version injection
├── cmd/
│   ├── root.go                     # Root 'cloud' command with --version
│   ├── manage.go                   # Parent 'manage' subcommand
│   ├── vm.go                       # 'vm' parent command
│   ├── vm_common.go                # Shared VM flags and helpers
│   ├── vm_start.go                 # vm start
│   ├── vm_stop.go                  # vm stop
│   ├── vm_power.go                 # vm shutdown/restart/reset
│   ├── vm_info.go                  # vm status/config
│   ├── vm_snapshot.go              # vm snapshot create/list/delete/revert
│   ├── setup_vm_template.go        # setup-vm-template (core command)
│   ├── delete_vm_template.go       # delete-vm-template
│   ├── list_vms.go                 # list-vms (excludes templates)
│   ├── list_vm_templates.go        # list-vm-templates (only templates)
│   ├── list_storage.go             # list-storage (with shared detection)
│   ├── move_template.go            # move-template (to shared storage)
│   ├── replicate_template.go       # replicate-template (to all nodes)
│   ├── init_node.go                # init-node (dnsmasq for SDN)
│   └── setup_nfs.go                # setup-nfs (NFS server + client)
├── internal/
│   ├── executor/
│   │   └── executor.go             # LocalExecutor (os/exec via sh -c)
│   └── ssh/
│       └── client.go               # SSH client (golang.org/x/crypto/ssh)
├── go.mod                          # Module: cloud
├── go.sum
├── Makefile                        # Build automation
├── README.md                       # User documentation
└── AGENTS.md                       # Agent reference
```

### Module Name

```
module cloud
```

All internal imports use `cloud/internal/...` and `cloud/cmd/...`.

### Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/spf13/cobra` | CLI framework |
| `golang.org/x/crypto/ssh` | SSH client for remote execution |

---

## Core Patterns

### CommandExecutor Interface

All commands use this interface to abstract command execution:

```go
type CommandExecutor interface {
    Run(command string) (string, string, error)  // returns stdout, stderr, error
}
```

Two implementations:

1. **LocalExecutor** (`internal/executor/executor.go`): Runs commands via `sh -c`
2. **sshExecutor** (`cmd/setup_vm_template.go`): Wraps `ssh.Client` to implement the interface

### createExecutor Helper

Defined in `cmd/setup_vm_template.go`, used by all commands:

```go
func createExecutor() (CommandExecutor, func() error, error)
```

Returns an executor, a closer function, and an error. Checks `localMode` flag to decide between local and SSH execution.

### Shared Flag Variables

All commands that support remote/local modes share these package-level variables (defined in `cmd/setup_vm_template.go`):

```go
var (
    localMode      bool
    sshHost        string
    sshPort        int
    sshUsername    string
    sshPassword    string
    sshPrivateKey  string
    sshInsecure    bool
)
```

Each command registers these flags in its `init()` function.

### SSH Client

Located in `internal/ssh/client.go`:

- Connects via password or private key authentication
- Uses `InsecureIgnoreHostKey()` for host key verification (configurable via `--ssh-insecure`)
- 30-second connection timeout
- `Run(command)` returns `(stdout, stderr, error)` — both outputs are trimmed of whitespace

### Local Executor

Located in `internal/executor/executor.go`:

- Runs commands via `exec.Command("sh", "-c", command)`
- Uses `CombinedOutput()` to capture stdout and stderr together
- **Important**: Must use `sh -c` (not `strings.Fields`) to support shell constructs like `||`, `&&`, pipes, and quoting

---

## VM Commands (`cloud vm`)

`vm` is a **top-level command** (registered to `rootCmd`, not `manageCmd`).

All VM commands use the `addCommonVMFlags()` helper which registers `--local`, `--ssh-host`, `--ssh-password`, `--ssh-private-key`, `--ssh-port`, `--ssh-username`, and `--ssh-insecure` flags. They also share the `createExecutor()` helper from `setup_vm_template.go`.

### `vm start <vmid>`

Runs: `qm start <vmid>`

### `vm stop <vmid>`

Runs: `qm stop <vmid>`

Hard power off. VM is stopped immediately without graceful shutdown.

### `vm shutdown <vmid>`

Runs: `qm shutdown <vmid>`

Graceful ACPI shutdown. Requires QEMU guest agent for best results.

### `vm restart <vmid>`

Runs: `qm reboot <vmid>`

**Note**: The Proxmox command is `qm reboot`, NOT `qm restart`.

### `vm reset <vmid>`

Runs: `qm reset <vmid>`

Hard reset (equivalent to pressing the reset button).

### `vm status <vmid>`

Runs: `qm status <vmid>`

Output: `status: running` or `status: stopped`

### `vm config <vmid>`

Runs: `qm config <vmid>`

Output: Full VM configuration in key:value format.

### `vm snapshot create <vmid> <name>`

Runs: `qm snapshot <vmid> <name> [--description <desc>]`

Optional `--description` flag for snapshot notes.

### `vm snapshot list <vmid>`

Runs: `qm listsnapshot <vmid>`

Output: Tree format showing snapshot hierarchy with `current` marker.

### `vm snapshot delete <vmid> <name>`

Runs: `qm delsnapshot <vmid> <name>`

### `vm snapshot revert <vmid> <name>`

Runs: `qm rollback <vmid> <name>`

Reverts the VM to the specified snapshot state.

---

## Commands

### 1. `cloud manage setup-vm-template`

**Purpose**: Create a VM template on a Proxmox node from a cloud image.

**Required flags**: `--proxmox-node`, `--image-url`, `--template-id`, `--template-name`

**Authentication**: Either `--local` OR (`--ssh-host` + password/key)

**Workflow** (executed on target node):

```bash
# 1. Download image
wget -q --show-progress -O <image> '<url>' || curl -fSL -o <image> '<url>'

# 2. Create VM
qm create <vmid> --name tpl-<template-id> --memory <mem> --cores <cores> --net0 virtio,bridge=vmbr0

# 3. Import disk
qm importdisk <vmid> <image> <storage>

# 4. Attach disk
qm set <vmid> --scsihw virtio-scsi-pci --scsi0 <storage>:vm-<vmid>-disk-0

# 5. Add Cloud-Init drive
qm set <vmid> --ide2 <storage>:cloudinit

# 6. Set boot disk
qm set <vmid> --boot c --bootdisk scsi0

# 7. Configure serial console
qm set <vmid> --serial0 socket --vga serial0

# 8. Set Cloud-Init user
qm set <vmid> --ciuser <cloud-user>

# 9. Convert to template
qm template <vmid>

# 10. Cleanup
rm -f <image>
```

**VM ID generation**: If `--vm-id` is not provided, generates `9000 + (unix_timestamp % 1000)`.

**Key implementation detail**: The `qm importdisk` command has no Proxmox API equivalent — this is why SSH/local execution is required instead of REST API calls.

---

### 2. `cloud manage delete-vm`

**Purpose**: Delete a VM template via `qm destroy`.

**Required flags**: `--vm-id`

**Workflow**:

```bash
qm destroy <vmid>
```

No confirmation prompt — deletes immediately.

---

### 3. `cloud manage list-vms`

**Purpose**: List VMs, excluding templates.

**How template detection works**: `qm list` output does NOT distinguish templates from stopped VMs (both show status `stopped`). The command runs `qm config <vmid>` for each VM and checks for a `template:` line in the config output.

**Workflow**:

```bash
# Get all VMs
qm list

# For each VM, check if it's a template
qm config <vmid>
# If output contains "template:" line → it's a template → exclude from list
```

**Output format**: Table with VMID, NAME, STATUS, MEMORY columns.

---

### 4. `cloud manage list-vm-templates`

**Purpose**: List only VM templates.

**Same detection method as `list-vms`**: Checks `qm config <vmid>` for `template:` line.

**Output format**: Same table format, shows only templates.

---

### 5. `cloud manage list-storage`

**Purpose**: List storage and identify which is shared across the cluster.

**How shared detection works**:

1. Reads `/etc/pve/storage.cfg`
2. Parses storage entries (format: `type: name`)
3. Marks as shared if type is: `nfs`, `cephfs`, `rbd`, `glusterfs`, `iscsi`, `lvm`
4. **Unmarks** if the entry has a `nodes` restriction (node-local storage)

**Workflow**:

```bash
# Get storage status
pvesm status

# Get storage config
cat /etc/pve/storage.cfg
```

**Output**: Table with STORAGE, TYPE, STATUS, TOTAL, USED, SHARED columns.

---

### 6. `cloud manage move-template`

**Purpose**: Move a VM template's disk to shared storage.

**Required flags**: `--vm-id`, `--target`

**Workflow**:

```bash
# 1. Check current disk location
qm config <vmid> | grep '^scsi0:'

# 2. Convert template back to VM (remove template flag)
qm template <vmid> --delete 1 2>/dev/null || sed -i '/^template:/d' /etc/pve/qemu-server/<vmid>.conf

# 3. Move disk
qm move-disk <vmid> scsi0 <target-storage>

# 4. Convert back to template
qm template <vmid>
```

**Key detail**: `qm template --delete 1` is not supported on all Proxmox versions. Falls back to directly editing `/etc/pve/qemu-server/<vmid>.conf` to remove the `template:` line.

---

### 7. `cloud manage replicate-template`

**Purpose**: Clone a template to all cluster nodes.

**Required flags**: `--vm-id`, `--template-id`, `--template-name`

**Node discovery**: Uses `pvecm nodes` output. Parses lines like `1 1 dev (local)` to extract node names.

**Workflow**:

```bash
# Discover nodes
pvecm nodes

# For each node:
qm clone <source-vmid> <target-vmid> --name tpl-<id>-<node> --target <node> --full 1
qm template <target-vmid>
```

**Limitation**: `qm clone --target` only works with shared storage. For local storage clusters, the command detects the error and skips the node with a helpful message suggesting `setup-vm-template` instead.

**Target VM IDs**: `<source-vmid> + 1000 + <node-index>`.

---

### 8. `cloud manage init-node`

**Purpose**: Initialize a Proxmox node for SwiftCloud SDN by setting up dnsmasq.

**Workflow**:

```bash
# 1. Update packages
apt update

# 2. Install dnsmasq
apt install dnsmasq -y

# 3. Disable default instance
systemctl disable --now dnsmasq

# 4. Verify
dpkg -s dnsmasq | grep 'Status: install ok installed'
systemctl is-enabled dnsmasq
```

**Why**: The default dnsmasq instance conflicts with SwiftCloud SDN's DNS management. This installs it but keeps it disabled so SDN can manage DNS.

---

### 9. `cloud manage setup-nfs`

**Purpose**: Set up NFS shared storage for the Proxmox cluster.

**Two modes**:

#### Server Mode (`--server`)

Sets up an NFS server on the target node:

```bash
# Install
apt update && apt install -y nfs-kernel-server

# Create export directory
mkdir -p <share> && chmod 777 <share>

# Configure exports
echo '<share> *(rw,sync,no_subtree_check,no_root_squash)' >> /etc/exports

# Export and enable
exportfs -ra && systemctl restart nfs-kernel-server && systemctl enable nfs-kernel-server
```

#### Client Mode (default)

Configures NFS client and adds storage to Proxmox:

```bash
# Install client
apt update && apt install -y nfs-common

# Verify share
showmount -e <nfs-server>

# Add to Proxmox
pvesm add nfs <storage-name> --server <nfs-server> --export <share> --content <content>
```

#### All Nodes Mode (`--all-nodes`)

Installs `nfs-common` on all cluster nodes and adds NFS storage once (Proxmox replicates storage config via corosync):

```bash
# Discover nodes
pvecm nodes

# Install on each node
pvecm run <node> 'apt update && apt install -y nfs-common'

# Add storage (once — replicated via corosync)
pvesm add nfs <storage-name> --server <nfs-server> --export <share> --content <content>
```

**Content types**: `images,iso,vztmpl,backup,rootdir` (comma-separated, passed as-is to `pvesm`).

---

## Testing Approach

### Manual Testing Against Live Proxmox

The CLI is tested against a real Proxmox cluster:

```bash
# Test connectivity
cloud manage list-vms --ssh-host 100.126.79.49 --ssh-username root --ssh-password 'helloworld'

# Test template lifecycle
cloud manage setup-vm-template --ssh-host 100.126.79.49 --ssh-username root --ssh-password 'helloworld' \
  --proxmox-node pve --image-url 'https://...' --vm-id 9000 \
  --template-id test --template-name 'Test'

cloud manage list-vm-templates --ssh-host 100.126.79.49 --ssh-username root --ssh-password 'helloworld'

cloud manage delete-vm --ssh-host 100.126.79.49 --ssh-username root --ssh-password 'helloworld' --vm-id 9000
```

### Build Verification

```bash
make build          # Must compile without errors
./bin/cloud --help  # Must show help
./bin/cloud --version  # Must show version
```

---

## Build System

### Makefile Targets

| Target | Description |
|--------|-------------|
| `build` | Build for current platform → `bin/cloud` |
| `build-linux` | Build for Linux amd64 |
| `build-darwin` | Build for macOS (amd64 + arm64) |
| `build-windows` | Build for Windows amd64 |
| `build-all` | Build all platforms |
| `install` | Install to `$GOPATH/bin/cloud` |
| `run ARGS="..."` | Run without building binary |
| `test` | Run tests |
| `test-coverage` | Run tests with coverage report |
| `fmt` | Format code with `go fmt` |
| `lint` | Run `golangci-lint` |
| `deps` | Tidy and verify dependencies |
| `clean` | Remove build artifacts |

### Version Injection

```bash
go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
```

- `VERSION`: `git describe --tags --always --dirty`
- `COMMIT`: `git rev-parse --short HEAD`
- `DATE`: `date -u '+%Y-%m-%dT%H:%M:%SZ'`

---

## Common Pitfalls

### 1. `qm list --output-format json` not supported

Older Proxmox versions don't support `--output-format json`. Always parse the default tabular output manually.

### 2. Template detection via `qm list`

`qm list` shows templates as `stopped` — same as stopped VMs. Must check `qm config <vmid>` for the `template:` line.

### 3. `qm template --delete 1` not supported

Some Proxmox versions don't support `--delete 1`. Fall back to `sed -i '/^template:/d' /etc/pve/qemu-server/<vmid>.conf`.

### 4. `qm clone --target` requires shared storage

Cloning to a different node only works if the source VM is on shared storage. For local storage, use `setup-vm-template` on each node instead.

### 5. `pvesm add nfs` content format

The `--content` flag takes comma-separated values directly (e.g., `images,iso,vztmpl,backup`). Do NOT remove commas.

### 6. `pvesm set` doesn't support `--export`

When updating existing NFS storage, you cannot use `pvesm set` with `--export`. Only `pvesm add` supports it. Check existence first and skip or verify instead.

### 7. Local executor must use `sh -c`

Using `strings.Fields(command)` breaks shell constructs like `||`, `&&`, pipes. Always use `exec.Command("sh", "-c", command)`.

### 8. Module name mismatch

The module is `cloud`, not `github.com/...`. All internal imports must use `cloud/internal/...` or `cloud/cmd/...`. If `go mod tidy` tries to fetch from GitHub, the imports are wrong.

### 9. `pvecm nodes` output format

Output looks like:
```
Membership information
----------------------
    Nodeid      Votes Name
         1          1 dev (local)
         2          1 dev-02
```

Parse by skipping header lines, then the node name is the last field (strip `(local)` suffix).

### 10. Storage config parsing

`/etc/pve/storage.cfg` format:
```
nfs: nfs-shared
        export /srv/proxmox-nfs
        path /mnt/pve/nfs-shared
        server 100.126.79.49
        content vztmpl,images,backup,iso
```

- Section headers: `type: name`
- Properties are indented with tabs
- `nodes` property means node-local (not shared)

---

## Adding a New Command

1. Create `cmd/<command_name>.go`
2. Define a `var <name>Cmd = &cobra.Command{...}`
3. In `init()`, register with `manageCmd.AddCommand(<name>Cmd)`
4. Register shared flags (local mode, SSH) from `setup_vm_template.go`
5. Use `createExecutor()` to get an executor
6. Implement the workflow using `exec.Run("command")`
7. Build and test: `make build && ./bin/cloud manage <command> --help`

---

## Proxmox `qm` Command Reference

| Command | Purpose |
|---------|---------|
| `qm create <id>` | Create a new VM |
| `qm importdisk <id> <file> <storage>` | Import a disk image into a VM |
| `qm set <id> --<option> <value>` | Set VM configuration |
| `qm template <id>` | Convert VM to template |
| `qm template <id> --delete 1` | Convert template back to VM (not always supported) |
| `qm destroy <id>` | Delete a VM/template |
| `qm clone <id> <newid>` | Clone a VM |
| `qm move-disk <id> <disk> <storage>` | Move a disk to different storage |
| `qm config <id>` | Show VM configuration |
| `qm list` | List all VMs |

---

## Proxmox Storage Reference

| Command | Purpose |
|---------|---------|
| `pvesm status` | Show storage status |
| `pvesm status --storage <name>` | Show specific storage |
| `pvesm add nfs <name> --server <ip> --export <path> --content <types>` | Add NFS storage |
| `pvesm set <name> --<option> <value>` | Update storage config (no `--export`) |

---

## Cluster Management Reference

| Command | Purpose |
|---------|---------|
| `pvecm nodes` | List cluster nodes |
| `pvecm status` | Show cluster status (includes node IPs) |
| `pvecm run <node> '<cmd>'` | Run command on a specific cluster node |
