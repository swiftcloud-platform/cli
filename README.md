# cloud — the SwiftCloud CLI

Deploy apps, manage databases and work with object storage on [SwiftCloud](https://cloud.co.zm) from the terminal.

> **Status: rebuild in progress.** This repository is being rebuilt as the customer CLI for the Kubernetes-based platform. The plan, decisions and command tree are in [`docs/plan.md`](docs/plan.md). The previous Proxmox operator tool is preserved at tag [`v0-proxmox`](https://github.com/swiftcloud-platform/cli/releases/tag/v0-proxmox).

## Install

```sh
curl -fsSL https://cloud.co.zm/install.sh | sh
```

Or download a release for Linux, macOS or Windows from the [Releases](https://github.com/swiftcloud-platform/cli/releases) page.

## Use

```sh
cloud login                      # approve a code in your browser, once
cloud app create demo --image nginx --wait
cloud db create main --engine postgresql
cloud storage cp ./site s3://assets/ --recursive
```

Every command accepts `--output table|json|yaml` and `--quiet`. In CI, set `CLOUD_TOKEN`, `CLOUD_ORG` and `CLOUD_REGION` instead of a config file; `CLOUD_API_URL` points the CLI at another environment.

## Configuration

`~/.config/cloud/config.yaml` (`%APPDATA%\cloud\config.yaml` on Windows) holds named **contexts**:

```yaml
current_context: prod
contexts:
  prod:
    org: acme
    region: zm-lusaka-central-1
  staging:
    api_url: https://staging.cloud.co.zm/api/v1
    org: acme
```

Precedence, highest first: flag → `CLOUD_*` environment → active context → built-in default (`https://cloud.co.zm/api/v1`). Tokens are never stored in this file.

## Develop

```sh
make build      # ./bin/cloud
make test       # go test -race ./...
make cross      # every release target into ./bin
make help
```

Go 1.25. Releases are cut by goreleaser from a `v*` tag (see `.goreleaser.yaml` and `.github/workflows/release.yml`).
