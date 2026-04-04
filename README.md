# SwiftCloud CLI

A command-line tool for managing SwiftCloud infrastructure on Proxmox VE clusters.

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

### Install System-Wide

```bash
sudo cp bin/cloud /usr/local/bin/cloud
```

## Quick Start

```bash
# Show version
cloud --version

# List all commands
cloud --help
cloud manage --help
cloud vm --help
```

---

## Commands

### `cloud vm`

Manage virtual machines on Proxmox nodes. All VM commands support both remote (SSH) and local execution modes.

#### Lifecycle Commands

| Command | Description | Proxmox Command |
|---------|-------------|-----------------|
| `cloud vm start <vmid>` | Start a VM | `qm start <vmid>` |
| `cloud vm stop <vmid>` | Hard power off | `qm stop <vmid>` |
| `cloud vm shutdown <vmid>` | Graceful shutdown (ACPI) | `qm shutdown <vmid>` |
| `cloud vm restart <vmid>` | Restart a running VM | `qm reboot <vmid>` |
| `cloud vm reset <vmid>` | Hard reset (reset button) | `qm reset <vmid>` |

#### Information Commands

| Command | Description | Proxmox Command |
|---------|-------------|-----------------|
| `cloud vm status <vmid>` | Get VM status | `qm status <vmid>` |
| `cloud vm config <vmid>` | Get full VM config | `qm config <vmid>` |

#### Snapshot Commands

| Command | Description | Proxmox Command |
|---------|-------------|-----------------|
| `cloud vm snapshot create <vmid> <name>` | Create a snapshot | `qm snapshot <vmid> <name>` |
| `cloud vm snapshot list <vmid>` | List snapshots | `qm listsnapshot <vmid>` |
| `cloud vm snapshot delete <vmid> <name>` | Delete a snapshot | `qm delsnapshot <vmid> <name>` |
| `cloud vm snapshot revert <vmid> <name>` | Revert to snapshot | `qm rollback <vmid> <name>` |

#### Examples

```bash
# Start a VM
cloud vm start 100 \
  --ssh-host 192.168.1.100 --ssh-username root --ssh-password 'pass'

# Graceful shutdown
cloud vm shutdown 100 \
  --ssh-host 192.168.1.100 --ssh-username root --ssh-password 'pass'

# Get VM configuration
cloud vm config 100 \
  --ssh-host 192.168.1.100 --ssh-username root --ssh-password 'pass'

# Create a snapshot
cloud vm snapshot create 100 pre-update \
  --ssh-host 192.168.1.100 --ssh-username root --ssh-password 'pass' \
  --description "Before system update"

# List snapshots
cloud vm snapshot list 100 \
  --ssh-host 192.168.1.100 --ssh-username root --ssh-password 'pass'

# Revert to snapshot
cloud vm snapshot revert 100 pre-update \
  --ssh-host 192.168.1.100 --ssh-username root --ssh-password 'pass'

# Delete snapshot
cloud vm snapshot delete 100 pre-update \
  --ssh-host 192.168.1.100 --ssh-username root --ssh-password 'pass'
```

#### VM Command Flags

All `vm` subcommands share these flags:

| Flag | Required | Description | Default |
|------|----------|-------------|---------|
| `--local` | No | Run commands locally | `false` |
| `--ssh-host` | Yes* | Proxmox host hostname or IP | |
| `--ssh-username` | No | SSH username | `root` |
| `--ssh-password` | Yes* | SSH password | |
| `--ssh-private-key` | Yes* | Path to SSH private key | |
| `--ssh-port` | No | SSH port | `22` |
| `--ssh-insecure` | No | Skip host key verification | `false` |

*Required when not using `--local`.

The `snapshot create` command also supports:

| Flag | Description |
|------|-------------|
| `--description` | Snapshot description |

---

### `cloud manage setup-vm-template`

Set up a VM template on a Proxmox node by downloading a cloud image, creating a VM, configuring Cloud-Init, and converting it to a template.

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
cloud manage setup-vm-template --local \
  --proxmox-node pve1 \
  --image-url 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' \
  --vm-id 9000 \
  --template-id ubuntu-24-04 \
  --template-name 'Ubuntu 24.04 LTS (Noble)' \
  --family-id ubuntu
```

#### Flags

| Flag | Required | Description | Default |
|------|----------|-------------|---------|
| `--local` | No | Run commands locally instead of via SSH | `false` |
| `--ssh-host` | Yes* | Proxmox host hostname or IP | |
| `--ssh-username` | No | SSH username | `root` |
| `--ssh-password` | Yes* | SSH password | |
| `--ssh-private-key` | Yes* | Path to SSH private key | |
| `--ssh-port` | No | SSH port | `22` |
| `--proxmox-node` | Yes | Proxmox node name | |
| `--proxmox-storage` | No | Storage name | `local-lvm` |
| `--image-url` | Yes | URL to download cloud image | |
| `--image-name` | No | Filename (auto-extracted from URL) | |
| `--vm-id` | No | Proxmox VM ID (auto-generated if omitted) | 9000+ |
| `--template-id` | Yes | Unique slug for the template | |
| `--template-name` | Yes | Display name | |
| `--family-id` | No | OS family ID | |
| `--vm-memory` | No | Memory in MB | `2048` |
| `--vm-cores` | No | CPU cores | `2` |
| `--cloud-user` | No | Cloud-Init username | `swift` |

*Required when not using `--local`.

#### What It Does

1. Downloads the cloud image via `wget`/`curl`
2. Creates a VM: `qm create <id> --name tpl-<id> --memory <mem> --cores <cores> --net0 virtio,bridge=vmbr0`
3. Imports the disk: `qm importdisk <id> <image> <storage>`
4. Attaches the disk: `qm set <id> --scsihw virtio-scsi-pci --scsi0 <storage>:vm-<id>-disk-0`
5. Adds Cloud-Init drive: `qm set <id> --ide2 <storage>:cloudinit`
6. Sets boot disk: `qm set <id> --boot c --bootdisk scsi0`
7. Configures serial console: `qm set <id> --serial0 socket --vga serial0`
8. Sets Cloud-Init user: `qm set <id> --ciuser <user>`
9. Converts to template: `qm template <id>`
10. Cleans up the downloaded image

---

### `cloud manage delete-vm`

Delete a VM template from a Proxmox node by running `qm destroy`.

#### Usage

```bash
# Remote
cloud manage delete-vm \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword' \
  --vm-id 9000

# Local
cloud manage delete-vm --local --vm-id 9000
```

#### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--vm-id` | Yes | Proxmox VM ID of the template to delete |
| `--local` | No | Run commands locally |
| SSH flags | Yes* | Same as `setup-vm-template` |

---

### `cloud manage list-vms`

List VMs on a Proxmox node (excludes templates).

#### Usage

```bash
cloud manage list-vms \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword'
```

#### Output

```
📋 Listing VMs (mode: remote (SSH))...

VMID     NAME                           STATUS       MEMORY    
-------- ------------------------------ ------------ ----------
100      web-server-01                  running      4096      
101      db-server-01                   stopped      8192      

Total: 2 VM(s)
```

---

### `cloud manage list-vm-templates`

List VM templates on a Proxmox node.

#### Usage

```bash
cloud manage list-vm-templates \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword'
```

#### Output

```
📋 Listing VM templates (mode: remote (SSH))...

VMID     NAME                           STATUS       MEMORY    
-------- ------------------------------ ------------ ----------
9000     tpl-ubuntu-24-04               stopped      2048      
9001     tpl-debian-12                  stopped      2048      

Total: 2 template(s)
```

---

### `cloud manage list-storage`

List storage on a Proxmox node and identify which storage is shared across the cluster.

#### Usage

```bash
cloud manage list-storage \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword'
```

#### Output

```
📋 Listing storage (mode: remote (SSH))...

STORAGE              TYPE         STATUS     TOTAL        USED       SHARED  
-------------------- ------------ ---------- ------------ ---------- --------
local                dir          active     98497780     4715680    No      
local-lvm            lvmthin      active     258551808    2999200    No      
nfs-shared           nfs          active     98498560     4730880    Yes     

Storage with SHARED=Yes is accessible to all nodes in the cluster.
```

#### Shared Storage Types

The following storage types are automatically marked as shared:
- `nfs` - Network File System
- `cephfs` - Ceph File System
- `rbd` - Ceph Block Device
- `glusterfs` - GlusterFS
- `iscsi` - iSCSI
- `lvm` - LVM (clustered)

Storage with a `nodes` restriction in `/etc/pve/storage.cfg` is marked as not shared.

---

### `cloud manage move-template`

Move a VM template's disk to shared storage so it's accessible to all nodes in the cluster.

#### Usage

```bash
cloud manage move-template \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword' \
  --vm-id 9000 \
  --target nfs-shared
```

#### What It Does

1. Checks current disk location via `qm config <id>`
2. Converts template back to VM (removes `template:` flag from config)
3. Moves disk: `qm move-disk <id> scsi0 <target-storage>`
4. Converts back to template: `qm template <id>`

#### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--vm-id` | Yes | VM ID of the template to move |
| `--target` | Yes | Target shared storage name |
| `--local` | No | Run commands locally |
| SSH flags | Yes* | Same as `setup-vm-template` |

---

### `cloud manage replicate-template`

Replicate a VM template to all nodes in the cluster by cloning it to each node.

#### Usage

```bash
cloud manage replicate-template \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword' \
  --vm-id 9000 \
  --template-id ubuntu-24-04 \
  --template-name 'Ubuntu 24.04 LTS' \
  --storage local-lvm
```

#### Output

```
🔄 Replicating template 9000 to all nodes (mode: remote (SSH))...

📡 Discovering cluster nodes...
   Found 2 node(s): dev, dev-02

📦 Cloning to node 'dev' (VM ID: 10000)...
   ✅ Template created on dev (VM ID: 10000)
📦 Cloning to node 'dev-02' (VM ID: 10001)...
   ⚠️  Cannot clone to 'dev-02': template uses local storage
   💡 For local storage, run setup-vm-template on each node instead

🎉 Replication complete: 1/2 node(s) have the template (1 skipped - local storage)
```

#### Notes

- `qm clone --target` only works with shared storage (Ceph, NFS, etc.)
- For clusters with only local storage, use `setup-vm-template` on each node instead
- New template VM IDs are assigned as `<source-vm-id> + 1000 + <node-index>`

#### Flags

| Flag | Required | Description | Default |
|------|----------|-------------|---------|
| `--vm-id` | Yes | Source template VM ID | |
| `--template-id` | Yes | Template ID slug | |
| `--template-name` | Yes | Display name | |
| `--storage` | No | Target storage on each node | `local-lvm` |
| `--vm-memory` | No | Memory in MB | `2048` |
| `--vm-cores` | No | CPU cores | `2` |
| `--cloud-user` | No | Cloud-Init username | `swift` |
| `--family-id` | No | OS family ID | |
| `--local` | No | Run commands locally | |
| SSH flags | Yes* | Same as `setup-vm-template` | |

---

### `cloud manage init-node`

Initialize a Proxmox node for SwiftCloud SDN by installing and configuring dnsmasq.

#### Usage

```bash
# Remote
cloud manage init-node \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword'

# Local
cloud manage init-node --local
```

#### What It Does

1. Updates package lists: `apt update`
2. Installs dnsmasq: `apt install dnsmasq -y`
3. Disables default instance: `systemctl disable --now dnsmasq`
4. Verifies installation and service status

#### Why

This is required for SwiftCloud SDN to work correctly. The default dnsmasq instance conflicts with SDN's DNS management.

#### Flags

| Flag | Required | Description | Default |
|------|----------|-------------|---------|
| `--local` | No | Run commands locally | `false` |
| SSH flags | Yes* | Same as `setup-vm-template` | |

---

### `cloud manage setup-nfs`

Set up NFS shared storage for the Proxmox cluster.

#### Step 1: Set up NFS Server

Run this on the node that will host the NFS share:

```bash
cloud manage setup-nfs \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword' \
  --server \
  --share /srv/proxmox-nfs \
  --content images,iso,vztmpl,backup
```

This:
1. Installs `nfs-kernel-server`
2. Creates the export directory
3. Configures `/etc/exports` with `*(rw,sync,no_subtree_check,no_root_squash)`
4. Exports shares and enables the service

#### Step 2: Configure NFS Client on All Nodes

```bash
cloud manage setup-nfs \
  --ssh-host 192.168.1.100 \
  --ssh-username root \
  --ssh-password 'yourpassword' \
  --nfs-server 192.168.1.100 \
  --share /srv/proxmox-nfs \
  --storage-name nfs-shared \
  --all-nodes
```

This:
1. Discovers all cluster nodes via `pvecm nodes`
2. Installs `nfs-common` on all nodes (via `pvecm run`)
3. Adds NFS storage to Proxmox via `pvesm add nfs` (automatically replicated via corosync)
4. Verifies storage status

#### Flags

| Flag | Required | Description | Default |
|------|----------|-------------|---------|
| `--server` | No | Set up NFS server on target node | `false` |
| `--nfs-server` | Yes* | NFS server IP/hostname | |
| `--share` | No | NFS export/share path | `/srv/proxmox-nfs` |
| `--content` | No | Content types (comma-separated) | `images,iso,vztmpl,backup` |
| `--storage-name` | No | Proxmox storage name | `nfs-shared` |
| `--all-nodes` | No | Configure on all cluster nodes | `false` |
| SSH flags | Yes | Same as `setup-vm-template` | |

*Required when not using `--server`.

#### Content Types

| Type | Description |
|------|-------------|
| `images` | VM disk images |
| `iso` | ISO images |
| `vztmpl` | LXC templates |
| `backup` | VM/LXC backups |
| `rootdir` | Container root directories |

---

## Common Cloud Images

| OS | URL |
|----|-----|
| Ubuntu 24.04 LTS | `https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img` |
| Debian 12 | `https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2` |
| Fedora 42 | `https://download.fedoraproject.org/pub/fedora/linux/releases/42/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2` |

---

## Workflows

### Setting Up a New Cluster

```bash
# 1. Initialize each node for SDN
cloud manage init-node --ssh-host <node-ip> --ssh-username root --ssh-password 'pass'

# 2. Set up NFS shared storage
cloud manage setup-nfs --ssh-host <primary-node> --ssh-username root --ssh-password 'pass' --server
cloud manage setup-nfs --ssh-host <primary-node> --ssh-username root --ssh-password 'pass' \
  --nfs-server <primary-node> --share /srv/proxmox-nfs --storage-name nfs-shared --all-nodes

# 3. Create VM templates
cloud manage setup-vm-template \
  --ssh-host <primary-node> --ssh-username root --ssh-password 'pass' \
  --proxmox-node pve1 --proxmox-storage nfs-shared \
  --image-url 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' \
  --vm-id 9000 --template-id ubuntu-24-04 --template-name 'Ubuntu 24.04 LTS' --family-id ubuntu

# 4. Verify
cloud manage list-storage --ssh-host <primary-node> --ssh-username root --ssh-password 'pass'
cloud manage list-vm-templates --ssh-host <primary-node> --ssh-username root --ssh-password 'pass'
```

### Moving Existing Templates to Shared Storage

```bash
# 1. Check current storage
cloud manage list-storage --ssh-host <node> --ssh-username root --ssh-password 'pass'

# 2. Move each template
cloud manage move-template --ssh-host <node> --ssh-username root --ssh-password 'pass' \
  --vm-id 9000 --target nfs-shared

# 3. Verify
cloud manage list-vm-templates --ssh-host <node> --ssh-username root --ssh-password 'pass'
```

---

## Development

```bash
# Build
make build

# Build for all platforms
make build-all

# Run with arguments
make run ARGS="manage --help"

# Test
make test

# Format code
make fmt

# Lint
make lint

# Tidy dependencies
make deps
```

## Project Structure

```
cli/
├── cmd/
│   ├── root.go                   # Root 'cloud' command
│   ├── manage.go                 # 'manage' subcommand
│   ├── vm.go                     # 'vm' parent command
│   ├── vm_common.go              # Shared VM command helpers
│   ├── vm_start.go               # vm start
│   ├── vm_stop.go                # vm stop
│   ├── vm_power.go               # vm shutdown/restart/reset
│   ├── vm_info.go                # vm status/config
│   ├── vm_snapshot.go            # vm snapshot create/list/delete/revert
│   ├── setup_vm_template.go      # setup-vm-template
│   ├── delete_vm_template.go     # delete-vm-template
│   ├── list_vms.go               # list-vms
│   ├── list_vm_templates.go      # list-vm-templates
│   ├── list_storage.go           # list-storage
│   ├── move_template.go          # move-template
│   ├── replicate_template.go     # replicate-template
│   ├── init_node.go              # init-node
│   └── setup_nfs.go              # setup-nfs
├── internal/
│   ├── executor/
│   │   └── executor.go           # Local command executor
│   └── ssh/
│       └── client.go             # SSH client
├── main.go
├── Makefile
├── README.md
└── AGENTS.md
```
