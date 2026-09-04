# `cloud` — the SwiftCloud CLI: build plan

Decided 2026-09-04. This document is the source of truth for the rebuild; update it when a decision changes.

## Status

| Phase | State | Where |
|---|---|---|
| 0 Repo reset | done 2026-09-04 | `cli` `dbbdd35`, tag `v0-proxmox` |
| 1 Platform auth | done 2026-09-04 | app `059ee02` — device login, API tokens, `/api/v1/health`, `/api/v1/me` |
| 2 API core (apps) | done 2026-09-04 | app `e40a7ed`, `9942427` — `/api/v1/orgs`, `/regions`, apps CRUD/deploy/logs/domains, `/api/v1/openapi.json` |
| 3 CLI core | done 2026-09-04 | `cli` — login (device flow + `--token-stdin`), logout, whoami, org, region, app list/create/get/deploy/scale/logs/delete/domain; typed client generated from `api/openapi.json`; `--wait` with worker-heartbeat check; exit codes 2/3/4/5 |
| 4 Databases | API done 2026-09-04 (app `b28860a`: `/database-engines`, databases CRUD, start/stop/restart, logs, credentials, backups, enable, restore; client regenerated in `cli` `11bb526`); CLI done 2026-09-04 (`cli` `cb362a4`: the `cloud db` tree — list/create/get/delete, credentials with `--format env|url`, start/stop/restart, logs, backup enable/create/list, restore `--to`/`--at`, engines; `unsupported-engine`, `limit-reached` and `billing-blocked` mapped to exit 2). Restore-to-new is not yet exercised against a live cluster, since it provisions a real database. | |
| 5 Object storage + S3 | API done (app `5756b4e`); CLI done 2026-09-04 | `cli` — `internal/s3` (URIs, client, presigning, transfer planning) and the `cloud storage` tree: bucket list/create/get/delete/credentials, ls/cp/sync/mv/rm/cat/stat/presign. Verified against the live region endpoint with the `pics` bucket: round trip, idempotent sync, `--delete`, recursive download byte-identical including binary, locally signed presigned URL fetched by curl. **Multipart uploads fail on the region edge** — an 80 MiB `UploadPart` returns 502 after three attempts, so any single object above 64 MiB cannot be uploaded yet; that is infrastructure, not CLI. Credential caching in the keychain is not implemented: one API call per command, no object-storage secret at rest. |
| 6 Release | not started | |

**Phases 1 and 2 are built but not reachable** (checked 2026-09-04, after `cloud login` returned
`HTTP 404`). They live on `feat_new_cloud` in the platform repo (`swiftcloud-platform/cloud`, local
checkout `../test/cloud`), which is 98 commits ahead of `main`; `059ee02` is on that branch only.
`cloud.co.zm` still serves the pre-migration `main` — it sets an `__Secure-authjs.callback-url`
cookie, so it is the Auth.js build from before better-auth, and every `/api/*` path falls through to
the SvelteKit SSR handler and answers 404 with HTML. No merge date is set: the branch is a
deliberate rewrite and go-live is also blocked on the infra side (tailnet-only API server, missing
kubeconfig scopes), so do not assume `cloud.co.zm` serves `/api/v1` soon.

**Develop and test against the dev server**, not production: `CLOUD_API_URL=http://localhost:5173/api/v1`
runs `feat_new_cloud` with the worker up, and the device flow works there — `cloud login` prints a
code and the `/device` page approves it. For a scripted token, sign in at `/dash`, mint one under
Settings → API tokens, then `echo "$TOKEN" | cloud login --token-stdin`. (`../swiftcloud-frontend`
and `../swiftcloud-backend` are unrelated scaffolds — not the platform.)

## Decisions

| Question | Decision |
|---|---|
| Stack | Go + Cobra, in this repo. Proxmox-era commands retired to tag `v0-proxmox`, then removed from `main`. |
| Authentication | Device-code login (`cloud login`) via better-auth's `device-authorization` plugin, already shipped in the platform's better-auth 1.6.26. Long-lived, revocable API tokens for CI, created in the dashboard. |
| v1 scope | Apps, managed databases, object storage including S3 object operations (`ls`, `cp`, `sync`, `rm`, presign). Virtual machines follow in v1.1. |
| API home | Inside the SvelteKit app at `/api/v1`, wrapping the existing service classes. Not mentioned on marketing pages until Arthur says so. |
| API host | `cloud.co.zm` — no separate API hostname. The CLI ships with `https://cloud.co.zm/api/v1` as its compiled-in default; each context can override it, and `CLOUD_API_URL` overrides everything (staging, local dev). The device-flow verification URL is derived from the same base, so pointing the CLI at staging also sends the browser to staging. |
| API-token lifetime | Default 7 days. Chosen per token at creation (`--expires 90d`, dashboard picker), maximum 1 year; `whoami` shows the expiry and the CLI warns in the last 24 hours. Session tokens from `cloud login` keep the platform's 7-day sliding expiry. |
| Deploys | Image-only. `cloud app deploy` takes a registry reference; deploy-from-source (Dockerfile → registry) is not in v1. |
| Platforms | Cross-compiled for Linux, macOS and Windows, each on amd64 and arm64, from one goreleaser config. Linux and macOS are first-class: `install.sh`, Homebrew tap, OS keychain. Windows ships from the same release, best-effort at launch: token file under `%APPDATA%\cloud`, download from Releases, winget/scoop after launch. (Developers' machines running the CLI, not Windows VMs.) |

## Where we start

**This repo today** is a Proxmox VE operator tool: ~2,300 lines that shell `qm …` over SSH for VM templates, snapshots, NFS and dnsmasq. None of it applies to the Kubernetes platform (KubeVirt, Knative, CNPG, SeaweedFS). Keep: the module name `cloud`, Cobra, the Makefile with version/commit/date injection, the binary name. Everything under `cmd/` goes.

**The platform today** has no API a CLI could call. The dashboard mutates through 20 SvelteKit form-action files; the 11 JSON routes that exist are ad hoc (VM power, database logs, coupon redemption, SQL editor). Authentication is a session cookie only — magic link or Google — and there is no token model in the schema. But every operation already lives in a service class, so the API is a thin, guarded layer over code that works:

| Service | Methods the API will expose |
|---|---|
| `app.service` | `createApp`, `listApps`, `getApp`, `updateApp`, `deleteApp`, `getDeployments`, `getAppLogs`, `addCustomDomain`, `listCustomDomains`, `removeCustomDomain`, `reconcileCustomDomainTls` |
| `database.service` | `createDatabase`, `listDatabases`, `getDatabase`, `deleteDatabase`, `startDatabase`, `stopDatabase`, `restartDatabase`, `getDatabaseLogs`, `getDatabaseCredentials`, `enableBackups`, `createBackup`, `listBackups`, `restoreToNewDatabase` |
| `object-storage.service` | `createBucket`, `listBuckets`, `getBucket`, `deleteBucket`, `getBucketCredentials`, `generatePresignedUrl` |

**Authorisation is already right and must be reused, not re-implemented.** `+server.ts` routes run no layout guard, so every handler self-guards through `requireOrgMember`, `requireAppAccess`, `requireOrgDatabase`, `requireBucketAccess` — the functions the 50-test authorisation suite covers. Roles: `CREATE_RESOURCE` and `DELETE_RESOURCE` are owner/admin; `UPDATE_RESOURCE` adds member; `VIEW_RESOURCE` adds viewer. Reading credentials is a write-level permission.

## Architecture

Three deliverables, in dependency order.

### A. Machine authentication on the platform

- Enable better-auth's `device-authorization` and `bearer` plugins. Endpoints arrive for free: `/device/code`, `/device`, `/device/approve`, `/device/deny`, `/device/token`. Options to set: `expiresIn` (10 min), `interval` (5 s), `userCodeLength` (8, grouped `XXXX-XXXX`), `validateClient` (accept only `cloud-cli`).
- Build the approval page at `/device` in the dashboard: shows the code the terminal printed, the requesting client, and Approve / Deny. Requires a signed-in session; a fresh browser goes through the normal sign-in first.
- `cloud login` result is a **session token** (7-day expiry, sliding by the platform's `updateAge` of 1 day). Good for a laptop, wrong for CI. So also add an `api_token` model: SHA-256 hash of the secret, org-scoped, bound to a role no higher than the creator's, expiry (default 7 days, chosen at creation, max 1 year), `lastUsedAt`, revocable. Dashboard page: Settings → API tokens, showing the secret exactly once. Prefix `sc_` so a leaked token is recognisable in scanners.
- One helper, `requireApiAuth(event)`, resolves identity from a session cookie, a bearer session token, or an `sc_` API token, then hands off to the existing `requireOrgMember` chain. Nothing under `/api/v1` calls Prisma for identity directly.

### B. `/api/v1` in the SvelteKit app

- Resource-oriented JSON, one `+server.ts` per collection and per item. Path carries the organisation: `/api/v1/orgs/{org}/apps/{app}`.
- Long operations return the resource with its `status`; the CLI polls. This matches the platform's design — the database row is desired state, the worker's sync processors advance status, and nothing else is allowed to. The API never fabricates "ready".
- `GET /api/v1/health` reports the worker heartbeat age, so a CLI `--wait` that sees nothing moving can say *"the worker is not running"* instead of timing out silently. (Statuses freeze, not fail, when the worker is down — the most confusing state the platform has.)
- Errors as `application/problem+json` (RFC 9457) with a stable `type` slug the CLI maps to exit codes. Creates accept `Idempotency-Key`. Lists paginate by cursor. Rate limit per token.
- Request and response shapes in Zod, emitted as an OpenAPI 3.1 document at `/api/v1/openapi.json`. The Go client types are generated from it (`oapi-codegen`), so the two sides cannot drift.
- Handler tests mirror the authorisation suite: every endpoint proves 401 anonymous, 403 wrong role, 404 foreign tenant, before it proves the happy path.

### C. The Go CLI

- Layout: `cmd/` (Cobra commands only, thin), `internal/api` (generated client + retry/backoff), `internal/auth` (device flow, token store), `internal/config` (contexts), `internal/output` (table / json / yaml / quiet), `internal/wait` (status polling with timeout and worker-heartbeat check), `internal/s3` (object operations, below).
- **S3 data path.** Object operations use `aws-sdk-go-v2` pointed at the region's S3 endpoint with the bucket's own access key — the CLI resolves the endpoint from `GET /regions` and the credentials from `GET …/buckets/{name}/credentials` once, then caches them in the keychain, keyed by bucket. Bytes never pass through the SvelteKit app: it would be a bandwidth bottleneck, and the platform's S3 auth (SeaweedFS `enableAuth` with per-org identities) already enforces isolation at the storage layer. Path-style addressing, region string `us-east-1`, multipart above 64 MiB with parallel parts, resumable `sync` by size + ETag, and presigned URLs signed locally (SigV4) so `presign` needs no API call.
- Config at `~/.config/cloud/config.yaml` with named **contexts** — API URL, organisation, default region — so staging and production coexist. Token stored in the OS keychain where available, else a `0600` file. Environment overrides for CI: `CLOUD_TOKEN`, `CLOUD_ORG`, `CLOUD_REGION`, `CLOUD_API_URL`.
- Every command takes `--output table|json|yaml` and `--quiet`; every mutating command takes `--wait` (poll to a terminal status, default timeout 10 min) and destructive ones require `--yes` or an interactive confirm that echoes the resource name.
- Tokens never appear as positional arguments (they land in shell history); `cloud login --token-stdin` for scripts.
- Release: goreleaser → GitHub Releases for linux/darwin/windows × amd64/arm64, `cloud update` self-update, `install.sh` served from `cloud.co.zm/install.sh`, Homebrew tap. Version, commit and date injected as today.

## Command tree (v1)

```
cloud login [--token-stdin]        device-code flow, or paste a CI token
cloud logout
cloud whoami                       user, organisation, region, token kind and expiry
cloud org   list | use <slug>
cloud region list

cloud app   list
cloud app   create  <name> --image <ref> [--port 8080] [--min 0 --max 3] [--size <tier>]
                    [--registry-server --registry-username --registry-password-stdin] [--wait]
cloud app   get     <name>
cloud app   deploy  <name> --image <ref> [--wait]      new image → new revision
cloud app   scale   <name> --min N --max N
cloud app   logs    <name> [-f] [--since 10m]
cloud app   delete  <name> [--yes]
cloud app   domain  add <name> <hostname>              prints the CNAME target for the app's region
cloud app   domain  list <name>
cloud app   domain  remove <name> <hostname>

cloud db    list
cloud db    create  <name> --engine postgresql|mariadb [--size <tier>] [--wait]
cloud db    get     <name>
cloud db    credentials <name> [--format env|url]      write-level permission; printed once, never logged
cloud db    start | stop | restart <name>
cloud db    logs    <name> [-f]
cloud db    backup  enable <name> [--retention 3d]
cloud db    backup  create <name>
cloud db    backup  list   <name>
cloud db    restore <name> --to <new-name> [--at 2026-09-04T10:15:00Z]   always a NEW database

cloud storage bucket list
cloud storage bucket create <name> [--region]
cloud storage bucket get    <name>
cloud storage bucket delete <name> [--yes]             purges objects; confirm echoes the name
cloud storage bucket credentials <name> --format env|aws-profile|rclone

cloud storage ls   [s3://bucket[/prefix]] [--recursive] [--human]   no argument lists buckets
cloud storage cp   <src> <dst> [--recursive]        local↔s3://, s3://↔s3://; multipart, parallel, progress bar
cloud storage sync <src> <dst> [--delete] [--dry-run]              size+ETag comparison, resumable
cloud storage mv   <src> <dst>
cloud storage rm   s3://bucket/key [--recursive] [--yes]
cloud storage cat  s3://bucket/key                  stream to stdout; `cp - s3://…` reads stdin
cloud storage stat s3://bucket/key                  size, ETag, content-type, last modified
cloud storage presign s3://bucket/key [--method GET|PUT] [--expires 1h]   signed locally, no API call

cloud completion bash|zsh|fish|powershell
cloud version
cloud update
```

Object operations mirror `aws s3` verbs so existing habits transfer, and `bucket credentials --format aws-profile|rclone` still exists for people who prefer those tools or need features we don't ship (server-side copy across providers, complex filters). The CLI is the default; the escape hatch stays.

## Gotchas to design around

Each of these has already bitten the platform once; the CLI must not rediscover them.

- **Statuses only move when the worker runs.** `--wait` polls, has a timeout, and checks `/health` for heartbeat age. "Deploying" for ten minutes with a dead worker is reported as such.
- **App URLs are `https`.** Knative reports the scheme it terminates (`http`); the platform normalises. The API returns the normalised URL; the CLI never builds one.
- **Custom domains are DNS-gated.** Let's Encrypt allows five failed validations per hostname per hour. `cloud app domain add` runs the same preflight the dashboard does and prints the region-specific CNAME target when the record is missing, instead of starting issuance.
- **Custom-domain TLS is not live yet.** `CertificateProvisioned=True reason=TLSNotEnabled` must never render as "SSL active" in `cloud app domain list`. Show the platform's `certificateReason` verbatim.
- **Credentials are read-once, write-permission.** `db credentials` and `bucket credentials` require `UPDATE_RESOURCE`; a viewer gets a clear 403 with the role that would work.
- **Restore creates, never overwrites.** `cloud db restore` always names the new database and says so before running.
- **Deleted names are reusable.** Soft delete releases the name (partial unique index); the confirm prompt echoes the name so the wrong one is not deleted by muscle memory.
- **Regions have their own kubeconfigs and credentials.** Every create takes `--region`, defaulting from the context; the API resolves per-region clients — the CLI never sees a kubeconfig.
- **Legacy hosts.** `SEO_LEGACY_HOSTS` redirects GET/HEAD only; the CLI targets `cloud.co.zm` explicitly and refuses a config whose API URL is a retired host.
- **SeaweedFS is S3-compatible, not S3.** Path-style URLs only (no virtual-hosted buckets), region literally `us-east-1`, no bucket versioning or lifecycle rules in the API, ETags of multipart objects are not MD5s — `sync` compares size + ETag, never assumes an MD5. Test against the real region endpoint in CI, not against MinIO.
- **Presigned URLs once carried the bucket name twice.** A dashboard bug this year produced `…/bucket/bucket/key`. The CLI signs locally with the SDK and has a golden test for the exact URL shape.
- **Storage credentials are per bucket, and the admin keys are never issued.** `bucket credentials` returns the bucket's own identity from the DB-reconciled S3 config; the CLI must never ask for or accept region admin keys.

## Phases

| # | Phase | Deliverable | Est. |
|---|---|---|---|
| 0 | Repo reset | Tag `v0-proxmox`; remove `cmd/*`; new `internal/` layout; goreleaser; CI (lint, test, cross-build); `install.sh`. | ½ day |
| 1 | Platform auth | Device-authorization + bearer plugins; `/device` approval page; `api_token` model, settings UI, `requireApiAuth`; tests. | 2–3 days |
| 2 | API core | `/me`, `/orgs`, `/regions`, `/health`; apps CRUD, deploy, logs, domains; problem+json; OpenAPI; handler tests. | 3–4 days |
| 3 | CLI core | login/logout/whoami, org, region, app; contexts; output formats; `--wait`; mock-server integration tests; golden tables. | 3–4 days |
| 4 | Databases | API + CLI incl. backups and restore-to-new. | 3–4 days |
| 5 | Object storage | API + CLI for buckets and credentials; `internal/s3` with `ls`, `cp`, `sync`, `mv`, `rm`, `cat`, `stat`, `presign`; multipart and progress; tests against the real region endpoint. | 5–6 days |
| 6 | Release | GitHub Releases, `install.sh`, Homebrew tap, `cloud update`; `/docs/cli` page when Arthur is ready to publish it; `llms.txt` entry. | 1–2 days |

Estimates are working days for one engineer with the platform already running locally. Phases 1–2 and 3 can run in parallel once the OpenAPI document exists; the S3 half of phase 5 depends only on phase 3 and a bucket credential, so it can start early.

## Definition of done (v1)

- On a fresh machine: `curl -fsSL https://cloud.co.zm/install.sh | sh`, `cloud login`, `cloud app create demo --image nginx --wait` prints an `https://` URL that answers 200 — in under five minutes.
- `cloud storage bucket create demo`, `cloud storage cp ./site s3://demo/ --recursive`, `cloud storage ls s3://demo --recursive` round-trips a 1 GiB directory with a byte-identical `sync` back, and a `presign` URL from the CLI opens in a browser.
- Every `/api/v1` handler has tests for anonymous, wrong-role and foreign-tenant before the happy path.
- `internal/` at or above 80 % coverage; `cmd/` exercised through golden-file tests.
- No token ever passes as a command-line argument; `--token-stdin` is the only scripted path.
- A dead worker is reported as a dead worker, never as a slow deploy.

## Later

- v1.1: virtual machines — create, power, console URL, public address attach (tier A), published ports.
- Deploy from source via buildpacks and a platform registry.
- `cloud db sql <name>` wrapping the SQL editor endpoint.
