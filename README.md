# SwiftCloud CLI

A command-line tool for managing SwiftCloud infrastructure.

## Installation

### From Source

```bash
cd cli
make build
```

The binary will be built to `bin/cloud`.

### Install to GOPATH

```bash
make install
```

This installs the `cloud` binary to `$GOPATH/bin/cloud`.

## Usage

### Basic Commands

```bash
# Show version
cloud --version

# Show help
cloud --help
```

### Managing VM Templates

The `manage setup-vm-template` command sets up a VM template on a Proxmox node. It supports two modes:

- **Remote mode (default):** SSHs into the Proxmox host and runs `qm` commands
- **Local mode (`--local`):** Runs `qm` commands directly (use when running on the Proxmox host itself)

#### Remote Mode (via SSH)

```bash
cloud manage setup-vm-template \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword' \
  --proxmox-node pve1 \
  --proxmox-storage local-lvm \
  --image-url 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' \
  --vm-id 9000 \
  --template-id ubuntu-24-04 \
  --template-name 'Ubuntu 24.04 LTS (Noble)' \
  --family-id ubuntu
```

#### Local Mode (run on Proxmox host)

```bash
cloud manage setup-vm-template \
  --local \
  --proxmox-node pve1 \
  --proxmox-storage local-lvm \
  --image-url 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' \
  --vm-id 9000 \
  --template-id ubuntu-24-04 \
  --template-name 'Ubuntu 24.04 LTS (Noble)' \
  --family-id ubuntu
```

#### Using SSH Key Authentication

```bash
cloud manage setup-vm-template \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-private-key ~/.ssh/id_ed25519 \
  --proxmox-node pve1 \
  --image-url 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' \
  --vm-id 9000 \
  --template-id ubuntu-24-04 \
  --template-name 'Ubuntu 24.04 LTS (Noble)' \
  --family-id ubuntu
```

## Command Reference

### `cloud manage setup-vm-template`

Sets up a VM template on a Proxmox node by running the documented `qm` commands:

1. `wget` / `curl` - Download the cloud image
2. `qm create` - Create the template VM
3. `qm importdisk` - Import the downloaded disk
4. `qm set` - Configure disk, Cloud-Init, boot, and serial console
5. `qm template` - Convert VM to template

#### Required Flags

| Flag | Description | Example |
|------|-------------|---------|
| `--proxmox-node` | Proxmox node name | `pve1` |
| `--image-url` | URL to download the cloud image | `https://cloud-images.ubuntu.com/...` |
| `--template-id` | Unique slug for the template | `ubuntu-24-04` |
| `--template-name` | Display name for the template | `Ubuntu 24.04 LTS (Noble)` |

#### Execution Mode

| Flag | Description |
|------|-------------|
| `--local` | Run `qm` commands locally instead of via SSH (use when running on the Proxmox host) |

When **not** using `--local`, these SSH flags are required:

| Flag | Description | Default |
|------|-------------|---------|
| `--ssh-host` | Proxmox host hostname or IP | (required) |
| `--ssh-username` | SSH username | `root` |
| `--ssh-password` | SSH password | (required if no key) |
| `--ssh-private-key` | Path to SSH private key | (required if no password) |
| `--ssh-port` | SSH port | `22` |
| `--ssh-insecure` | Skip host key verification | `false` |

#### Optional Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--proxmox-storage` | Storage name for the template | `local-lvm` |
| `--image-name` | Filename for the downloaded image | Extracted from URL |
| `--vm-id` | Proxmox VM ID for the template | Auto-generated (9000+) |
| `--family-id` | OS family ID | (empty) |
| `--vm-memory` | Memory in MB for the template VM | `2048` |
| `--vm-cores` | CPU cores for the template VM | `2` |
| `--cloud-user` | Default Cloud-Init username | `swift` |

## Common Cloud Images

### Ubuntu 24.04 LTS (Noble)
```
https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
```

### Debian 12 (Bookworm)
```
https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2
```

### Fedora 42
```
https://download.fedoraproject.org/pub/fedora/linux/releases/42/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2
```

## Development

### Building

```bash
# Build for current platform
make build

# Build for all platforms
make build-all

# Run with arguments
make run ARGS="manage setup-vm-template --help"
```

### Testing

```bash
make test
make test-coverage
```

### Code Quality

```bash
make fmt    # Format code
make lint   # Run linter
make deps   # Tidy dependencies
```

## Architecture

The CLI is built with:
- **Cobra** - Command-line interface framework
- **golang.org/x/crypto/ssh** - SSH client for remote Proxmox access
- **os/exec** - Local command execution for `--local` mode

### Project Structure

```
cli/
├── cmd/                          # Cobra commands
│   ├── root.go                   # Root 'cloud' command
│   ├── manage.go                 # 'manage' subcommand
│   └── setup_vm_template.go      # 'setup-vm-template' subcommand
├── internal/
│   ├── executor/
│   │   └── executor.go           # Local command executor
│   └── ssh/
│       └── client.go             # SSH client for Proxmox host
├── main.go                       # Entry point
├── Makefile                      # Build automation
└── README.md                     # This file
```
