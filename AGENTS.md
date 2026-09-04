# cloud CLI — agent reference

Read `docs/plan.md` first. It holds every decision (stack, auth, scope, API host, token lifetime, platforms) and the v1 command tree; do not re-decide those here.

## Layout

```
main.go                  entry point; version injected via -ldflags (main.version_, main.commit, main.date)
cmd/                     Cobra commands only. Thin: parse flags → call internal → print via output.
internal/config          contexts, CLOUD_* env, precedence flag > env > context > default; refuses retired hosts
internal/output          --output table|json|yaml and --quiet; anything printed goes through Printer
internal/version         build metadata, User-Agent
internal/api             (phase 3) generated client from the platform's OpenAPI document
internal/auth            (phase 3) device-code login, token store (keychain, else 0600 file)
internal/wait            (phase 3) status polling with timeout and worker-heartbeat check
internal/s3              (phase 5) object operations against the region's S3 endpoint
docs/plan.md             the plan; source of truth
```

## Rules that are not negotiable

- **Never fabricate state.** The platform advances resource status only through its worker. A command that waits polls the API and, when nothing moves, reports the worker heartbeat age from `/api/v1/health`. It never prints "ready" on its own judgement.
- **Tokens never appear in argv.** `cloud login --token-stdin` is the only scripted path. Nothing logs a token, a credential or a presigned URL at debug level.
- **Object bytes never go through the API.** `internal/s3` talks to the region's S3 endpoint with the bucket's own credentials.
- **Retired hosts are refused**, not redirected (`internal/config.ValidateAPIURL`). A 301 would turn a POST into a GET.
- **Destructive commands confirm by echoing the resource name** or take `--yes`. Soft-deleted names are reusable on the platform, so the name is the whole safeguard.
- **Exit codes are stable** (see `cmd/root.go`): 2 usage, 3 not signed in, 4 role denied, 5 not found.
- **Tests before behaviour.** Every package has `_test.go`; command tests reset persistent flags between runs (they are package globals).

## Toolchain

Go 1.25, Cobra, `gopkg.in/yaml.v3`. `make test`, `make cross`. CI lints with the latest golangci-lint; a locally installed older linter may refuse Go 1.25 — that is the linter, not the code.
