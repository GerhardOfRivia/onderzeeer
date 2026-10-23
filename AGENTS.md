# Repository guidance

## Project overview

onderzeeer is a durable file-triggered job runner: it watches directories, waits
for files to settle, persists jobs in SQLite, and executes command pipelines.
The supported release targets are Linux AMD64 and ARM64.

- `cmd/onderzeeer`: client entry point, including standalone foreground runs.
- `cmd/onderzeeerd`: background daemon entry point.
- `internal/cli`: CLI argument parsing and command behavior.
- `internal/config`: strict YAML decoding, defaults, validation, and path resolution.
- `internal/commandparse`: command tokenization and pipeline generation.
- `internal/watcher`: filesystem events, discovery, matching, and settling.
- `internal/queue`: SQLite persistence, migrations, claims, history, and recovery.
- `internal/executor`: process execution and platform-specific process handling.
- `internal/worker`: concurrent workers and sequential pipeline execution.
- `internal/resource`: shared resource coordination.
- `internal/daemon`: lifecycle and component wiring for an instance.
- `internal/control`: instance supervision, Unix-socket API, and client.
- `internal/webui`: HTTP dashboard API, OpenAPI specification, and embedded assets.
- `web`: React, TypeScript, MUI, and Vite frontend sources and tests.
- `demo` and `contrib`: examples and integration resources.

## Setup and commands

Run Go commands from the repository root. Use the Go version required by
`go.mod` or newer. If `.go/toolchain/bin/go` is installed, `source activate`
selects the local toolchain and repository-local caches. Use Node.js 22 for
frontend development.

| Task | Command |
| --- | --- |
| Build both binaries into `bin/` | `make build` |
| Format Go | `go fmt ./...` |
| Static checks | `go vet ./...` |
| Go tests | `go test ./...` |
| Race checks | `go test -race ./...` |
| All Go checks, including formatting | `make check` |
| Install frontend dependencies | `cd web && npm ci` |
| Frontend tests | `cd web && npm test` |
| Build embedded frontend | `cd web && npm run build` |
| Vite development server | `cd web && npm run dev` |

`make web` builds through Docker, but also runs `npm audit fix` and removes
frontend dependencies afterward. Prefer the direct npm commands for routine
changes to avoid incidental dependency updates.

## Implementation conventions

- Keep entry points thin and place behavior in the relevant `internal` package.
- Follow neighboring Go and TypeScript conventions; format changed Go files
  with `gofmt` and preserve TypeScript strict-mode compatibility.
- Add regression tests near changed behavior: Go tests use `*_test.go`;
  frontend tests use Vitest and React Testing Library in `web/src`.
- Keep test databases and filesystem fixtures in temporary directories, and
  clean up processes, sockets, and goroutines started by tests.
- Preserve platform-specific implementations and build constraints when
  changing process or filesystem behavior.
- Keep SQLite migrations and recovery behavior compatible with existing queues.
- Preserve the distinction between standalone `onderzeeer test <config>` and
  daemon-managed instances. Config paths are explicit; standalone runs own their
  queue database, while managed instances receive a daemon-assigned database.
- Commands are executed as programs with argument arrays; shell-like display
  output must not introduce implicit shell execution.

## API and frontend changes

- Keep `internal/webui/openapi.json` synchronized with dashboard API changes,
  along with relevant frontend types and clients in `web/src`.
- The dashboard HTTP API and Unix-socket control API are separate interfaces.
  Preserve authentication and request-header requirements for control actions.
- Edit frontend sources under `web`, then regenerate `internal/webui/dist` with
  `npm run build`. Include the generated asset changes; do not hand-edit bundles.
- Embedded assets are tracked so Go builds work without Node.js. CI rebuilds
  them and checks for differences. The frontend build also validates OpenAPI,
  type-checks TypeScript, and copies documentation licenses.

## Validation and delivery

Start with tests for the affected package or component, then run the relevant
broader checks. For Go behavior changes, run `go vet ./...` and `go test ./...`;
include race checks for concurrency changes. For frontend changes, run
`npm test` and `npm run build` from `web`. Documentation-only edits do not
require application tests.

Update `README.md` and affected examples when CLI, configuration, or API behavior
changes. Keep unrelated changes out of the patch, and report any checks that
could not run. Do not commit build binaries, caches, or `node_modules`.
`make release` creates and pushes a tag; run it only when explicitly requested.
