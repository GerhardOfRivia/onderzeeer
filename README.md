# onderzeeer

onderzeeer is a small, durable file-triggered job runner. It watches directories,
waits for matching files to stop changing, queues them in SQLite, and runs a
command pipeline for each file. Use `onderzeeer` to run and inspect jobs, and
`onderzeeerd` to manage background instances and an optional web dashboard.

written and designed with help from openai's (5.6 sol)

## quick install

```bash
curl -fsSL https://raw.githubusercontent.com/GerhardOfRivia/onderzeeer/refs/heads/main/install.sh | sh
```

onderzeeer supports Linux on AMD64 and ARM64. The installer places both `onderzeeer`
and `onderzeeerd` in the selected install directory.

## getting started

Run a config in the foreground:

```bash
onderzeeer test csv_pipeline.yaml
```

`test` takes a config path, runs the pipeline in the foreground, and owns
its standalone queue database. No instance name is needed. It does not contact
`onderzeeerd`. Press Ctrl-C to stop gracefully.

To manage an instance in the background, first start the daemon as the user that
should run the configured programs:

```bash
onderzeeerd
```

Then use the daemon-backed lifecycle commands from another terminal:

```bash
onderzeeer start csv_pipeline.yaml csv-pipeline
onderzeeer ps
onderzeeer stop csv-pipeline
```

`test` requires exactly one selected config. `start` derives a name from the
config filename when one is omitted. The daemon assigns each managed
instance its own queue database; only standalone runs use `database.path`.
Only daemon-managed instances appear in `ps`.

`test` keeps watching until interrupted or a worker fails. A pipeline or queue
error stops the standalone run with exit code 1 after persisting the attempt,
even when retries remain. A normal signal shutdown exits with code 0.

Config paths are always explicit; there is no automatic discovery or
`ONDERZEEER_CONFIG` fallback. Options may appear before or after positional
arguments. Use `--` for paths beginning with a dash, as in
`onderzeeer check -- -pipeline.yaml`.

## checking configuration

Validate a config and display each watch's pipeline without running it:

```bash
onderzeeer check csv_pipeline.yaml
onderzeeer check --raw csv_pipeline.yaml
```

`check` accepts a YAML file or directory without contacting the daemon, opening
the database, or acquiring resources. It resolves config-relative paths and
reusable `values`, leaving per-job templates such as `{{file}}` unexpanded. Steps
appear in execution order as shell-like command lines, with configured output
paths and resource requirements. This display does not imply shell execution;
use `--raw` to see each quoted program and JSON argument array.

## generating pipeline configuration

`parse` turns an already shell-tokenized command into a pipeline YAML fragment
without executing it. Use `--` to separate onderzeeer's options from the command:

```bash
onderzeeer parse -- docker run --rm --gpus all \
  --mount 'type=bind,source={{dir}},target=/input,readonly' \
  --env 'INPUT={{basename}}' \
  nvidia/cuda:12.8.1-base-ubuntu24.04 nvidia-smi --query-gpu=name
```

```yaml
pipeline:
  - name: docker-run
    executor: docker
    image: nvidia/cuda:12.8.1-base-ubuntu24.04
    container_args:
      - --rm
      - --gpus
      - all
    mounts:
      - source: '{{dir}}'
        target: /input
        options:
          - readonly
    container_env:
      INPUT: '{{basename}}'
    command: nvidia-smi
    command_args:
      - --query-gpu=name
```

Paste the fragment under a watch, adjusting its indentation. `--name` overrides
the generated step name. Docker and Podman `run` commands, including
`container run`, become structured fields. Unsupported environment or mount
forms stay in `container_args`; an ambiguous image boundary produces a warning
and preserves the whole invocation in raw `args`. Other runtime commands,
including Apptainer, remain raw. Ordinary executables become `command` entries
with `program` and `args`.

Basic `-v SOURCE:TARGET[:ro|rw]` bind mounts become `--mount`, which requires the
host source to exist. Converted relative sources resolve from the YAML file's
directory, not the directory where `parse` ran.

`parse`, `check`, and instance startup warn about Docker's interactive TTY flags
(`-it`, or `--interactive --tty`) and the invalid `--it` spelling. onderzeeer provides
no interactive stdin or TTY; remove these flags for unattended jobs. Warnings
leave the supplied arguments unchanged.

## managed instances

The daemon exposes its lifecycle API over a local Unix socket:

```text
onderzeeerd [--state-dir path] [--socket path] [--web-listen address] [--log-level level]
onderzeeer test <config>
onderzeeer start <config-or-instance> [name] [--socket path]
onderzeeer ps [--all] [--socket path]
onderzeeer stop [--socket path] id-or-name [id-or-name ...]
```

`start`, `ps`, and `stop` require a running daemon. `start` persistently registers
an instance before acknowledging success. `ps` lists running instances;
`ps --all` includes all registered stopped and failed instances. `stop` accepts
multiple IDs or names and persists the intention to stay stopped, including
when an instance has already failed.

The daemon stores `registry.sqlite` and `queues/<instance-id>.sqlite` in a
persistent state directory, selected by `--state-dir`, `ONDERZEEER_STATE_DIR`, or
`$XDG_STATE_HOME/onderzeeer` (default `~/.local/state/onderzeeer`). Only one daemon may
own a state directory, even if another socket is specified. Keep the complete
state directory, including SQLite companion files, on persistent storage.

On every daemon start, saved instances whose desired state is running resume
automatically with their stable IDs, names, queues, and validated configuration
snapshots. Shutting down the daemon preserves this intention; an explicit
`onderzeeer stop` clears it. Individual runner failures remain visible in `ps --all`
and do not prevent other instances from starting. Failed instances are retried
on the next daemon start; there is no automatic crash loop during a daemon run.

The original YAML need not remain available for recovery. `onderzeeer start NAME`
(or an ID) resumes the saved snapshot. To apply YAML edits, stop the instance and
start its config path again; this retains its ID and queue. Snapshot paths and
the default command working directory are fixed at registration. Referenced
watch directories, programs, and images must still be available to the daemon.

Start the daemon with `onderzeeerd`, then register each instance from another
terminal with `onderzeeer start <config> [name]`. The daemon accepts only its own
options; config paths, directories, and instance names belong to the client.
Every daemon start restores the saved registry; a new state directory starts
empty.

Managed queues are independent of the YAML's `database.path`. Existing standalone
or older-version queue files are left untouched and are not automatically
imported into new managed queues. Inspect those files with `--local`; registering
a new managed instance starts fresh queue history.

The foreground command is `onderzeeer test <config>`. It replaces `onderzeeer run`,
accepts no instance name, `--rm`, or `--socket`, and never registers an instance
with the daemon. It executes pipeline commands; use `onderzeeer check` to validate
and inspect a config without executing it.

Instance logs include a unique `instance_id` and a stable SHA-256 `config_hash`
of the effective configuration for comparing runs.

The socket used by each daemon-backed command is selected in this order:

1. Its explicit `--socket` value.
2. `ONDERZEEER_SOCKET`, when set.
3. `$XDG_RUNTIME_DIR/onderzeeer/onderzeeer.sock`, when `XDG_RUNTIME_DIR` is set.
4. `onderzeeer/onderzeeer.sock` beneath the operating system's per-user cache directory.
5. A UID-specific directory beneath the system temporary directory when no
   user cache directory is available.

Clients and daemon must use the same socket. Keep it private: access permits
starting programs as the daemon user. Prefer one daemon per user.

Inspect the running daemon's effective settings and file locations:

```bash
onderzeeer system
onderzeeer system --purge
```

`system` reports the daemon version, PID, start time, log level, control socket,
state directory, registry database, queue directory, lock files, web listener,
token file location, public-read setting, and active/inactive queue counts.
Settings come from the daemon, including its startup flags and environment;
the daemon has no separate configuration file. Use `--socket` to select it.

`--purge` permanently removes all exited and failed queue registrations and
their databases, jobs, history, and captured output. It first lists the queue
names, IDs, states, and database paths, then asks
`Are you sure you want to continue? [y/N]`. Enter `y` or `yes` to proceed;
Enter, any other answer, or end-of-input cancels without deleting anything.
No confirmation is needed when there are no inactive queues. Only the listed
queues are eligible; queues that change or restart before confirmation are
kept and reported for review. Running and stopping
instances are preserved. Config files and watched files remain untouched.
Purged instances no longer restore on daemon restart; register their config
again to create a fresh queue. A partial failure reports the affected queues
and exits with code 1; failed removals can be retried. Once file removal begins,
the retained registration is marked stopped so it cannot automatically resume
with a partly removed database.

## optional web dashboard

Enable the embedded dashboard with a loopback listener (disabled by default):

```bash
onderzeeerd --web-listen 127.0.0.1:8080
```

Alternatively, set `ONDERZEEER_WEB_LISTEN`. Open <http://127.0.0.1:8080> and paste
the token from the file named in the startup log. The file is beside the
control socket with suffix `.web-token` and mode `0600`; it is replaced at
startup and removed on clean shutdown. For the system service:

```bash
sudo cat /run/onderzeeer/onderzeeer.sock.web-token
```

The dashboard shows known queues, job counts, attempts, commands, and captured
output, including queues whose instances have stopped. It can restart known
queues and stop active instances. Its header shows the daemon build version
(`dev` when built without an override). Each entry in the instance list shows its
watch folders below the name and status. Job search and refresh remain on the
right of the job panel header. The queue API
(`GET /api/v1/queues`) returns `watches` as objects with `name` and `path` fields;
each path is the resolved folder from the registered configuration.
Use the information button beside a queue to open its instance details in the
left drawer, including its config path and hash. The selector below the job table
supports 10, 25, 50, or 100 jobs per page. Captured
output can be loaded once the job has succeeded or failed.

Search filters the entire queue before pagination and matches literal text in
file paths, watch names, job IDs (including `#123`), statuses, and error messages.
Click a watch name in the table to filter by that watch. Active watch and status
filters appear as removable tags beside the instance status and combine with the
search text. Changing a filter returns to the first page; selecting another
queue clears the filters. The jobs API accepts the same text as `search`.

To allow anyone who can reach the dashboard to browse without a token, set:

```bash
ONDERZEEER_WEB_PUBLIC_READ=true onderzeeerd --web-listen 127.0.0.1:8080
```

Public viewers see a **Read only** indicator and can browse queues, jobs,
attempts, instance history, command details, and captured stdout/stderr. This
includes file paths, command arguments, and logs. Start/Stop buttons are hidden,
and the API still requires a valid token for these actions. Choose **Unlock
controls** and enter the existing token to enable them; **Lock** returns to
public viewing. A rejected or expired token also returns to public viewing.

`ONDERZEEER_WEB_PUBLIC_READ` defaults to false when unset or empty, preserving
the login requirement for all API reads. It accepts boolean values (`true` or
`false`, also `1` or `0`); invalid values prevent daemon startup. Restart the
daemon after changing it. This setting does not enable the web listener by
itself or change its bind address. For Docker, pass
`--env ONDERZEEER_WEB_PUBLIC_READ=true`; with the supplied Compose file, set
`ONDERZEEER_WEB_PUBLIC_READ=true` in `.env`.

Keep the token private: it authorizes start/stop actions as the daemon user,
and reads when public viewing is disabled. Prefer loopback with an SSH tunnel.
To opt into network access, use a wildcard bind such as `0.0.0.0:8080` or
`[::]:8080` and connect by literal IP;
concrete non-loopback bind addresses and arbitrary HTTP hostnames are rejected.
Wildcard binds expose every interface, and HTTP traffic is unencrypted, so
restrict access with a firewall and protect the network path.

### API reference

With the web listener enabled, open `/docs/` on the same address as the
dashboard (for example, `http://127.0.0.1:8080/docs/`). The dashboard and login
screen also link to **API docs**. Swagger UI and its assets are embedded in the
daemon and work without an internet connection. Download the OpenAPI 3.1
document at `/openapi.json` or use **Download OpenAPI** in the reference.

The reference covers all eight dashboard HTTP endpoints under `/api/v1/`,
including filters, pagination, response schemas, examples, and error codes.
The served document reports the running build version and reflects
`ONDERZEEER_WEB_PUBLIC_READ`. The CLI's Unix-socket `/v1/` control API is separate
and is not covered by this reference.

The docs and specification are readable without a token. To make authenticated
requests, choose **Authorize** and paste the existing web token without the
`Bearer` prefix. Swagger UI keeps it in memory only; reloading the docs clears
it. Public viewing allows anonymous reads when enabled, but start/stop always
require a token. **Try it out** sends real requests to this daemon, including
start/stop actions. For those actions, keep the documented
`X-onderzeeer-Web: 1` header and `{}` JSON body.

The source specification is `internal/webui/openapi.json`. Update it alongside
API changes. The web build validates OpenAPI syntax, and Go tests check route
coverage, authentication declarations, and actual responses against the schemas.
Rebuild embedded assets with `make web` after changing the docs UI or its pinned
Swagger UI dependency.

## queue and history inspection

Inspect a managed instance by name, ID, or its registered config path:

```bash
onderzeeer status incoming
onderzeeer queue incoming
onderzeeer jobs incoming --status failed
onderzeeer jobs incoming --watch incoming
onderzeeer job incoming 42
onderzeeer logs incoming 42
onderzeeer job incoming 42 --rm
```

`status` prints counts, database/WAL sizes, reusable database space, and available
filesystem space; `queue` lists queued, pending, and running jobs; `jobs` lists job
history. `job` shows a job's runs and commands, and `logs` prints captured command
stdout and stderr. These commands read through `onderzeeerd` and accept `--socket`.
Stopped instances remain inspectable, even if their original YAML is gone.
A config-directory path selects its registered queues; select one instance when
a job ID exists in multiple queues.

`job <instance-or-config> <id> --rm` deletes a queued or finished job and all of
its run and command history, including captured output. Running and pending
jobs must be stopped first. Removal leaves the source file untouched and clears
its discovery record, so a later discovery may enqueue that file again. This
also works with `--local`, which opens the selected database for writing.

### storage maintenance

Job records are retained until explicitly removed. Captured output is retained
indefinitely unless output retention is enabled or cleanup is requested.
Each command attempt can retain roughly
2 MiB of captured output (1 MiB per stream), so log volume, pipeline length,
and retries determine storage growth.

Preview and prune old captured output from one queue:

```bash
onderzeeer prune incoming --older-than 30d --dry-run
onderzeeer prune incoming --older-than 30d
onderzeeer prune incoming --older-than 90d --include-failed
```

The required age accepts whole days (`30d`) or Go durations (`720h`, `90m`).
Only jobs completed strictly before the cutoff qualify. Successful jobs are
selected by default; `--include-failed` also selects failed jobs. Queued,
pending, and running jobs are always preserved, including their earlier attempts.
Cleanup clears captured stdout/stderr for all attempts of eligible jobs and
leaves a `captured output pruned` marker in the log view. It preserves job
records, duplicate detection, command/run metadata, errors, exit codes, and
watched or separately saved output files. It does not delete entire jobs.

`--dry-run` reports the matching command count and captured byte count without
changing history. These bytes are payload size, not a promise of disk space
reclaimed. Pruning commits small batches; if interrupted, committed batches
remain pruned and the command can safely be retried. Both maintenance commands
require exactly one queue and accept `--socket`, or `--local` with a standalone
config file. Their timeout defaults to 30 minutes; override it with `--timeout`.

For opt-in automatic retention, set these fields in the instance YAML:

```yaml
database:
  output_retention: 720h          # 30 days; omitted or 0s disables cleanup
  retention_include_failed: false
  warn_size_bytes: 10737418240    # database + WAL; default 10 GiB
  warn_free_percent: 10           # available filesystem space; default 10%
```

Retention runs at instance startup and hourly while the instance runs. It uses
the same pruning rules as the CLI. Errors are logged and retried on the next
pass. Warning thresholds are checked on the same schedule and reported by
`status` and `system`; set either threshold to zero to disable that warning.
Disk availability is reported on Linux and macOS. Storage warnings do not pause
jobs or delete data. Stopped queues can be maintained manually. For a managed
instance, stop it and start its config path again to apply YAML changes.

Pruning makes database pages reusable but does not shrink the database file.
To return unused space to the filesystem, stop the instance and compact it:

```bash
onderzeeer stop incoming
onderzeeer compact incoming
onderzeeer start incoming
```

Compaction runs SQLite `VACUUM` and truncates the WAL. It refuses active managed
instances and queues with running/pending jobs. Stop all standalone writers
before using `compact --local`. Compaction checks for available space of at
least twice the database plus WAL size when disk reporting is available;
it may take time for large databases. Automatic retention never compacts.

Use `--local` to inspect a standalone or legacy queue directly, without a daemon:

```bash
onderzeeer status --local incoming.yaml
onderzeeer logs --local incoming.yaml 42
```

Local inspection loads the selected YAML file or directory and opens each
`database.path` read-only. Missing databases are errors. It cannot be combined
with `--socket` and never silently replaces managed inspection.

## configuration

```yaml
queue:
  workers: 2
  max_retries: 3
  retry_delay: 10s

database:
  path: ./onderzeeer.db

values:
  shared_dir: /srv/onderzeeer

watches:
  - name: incoming
    path: ./incoming
    recursive: true
    process_existing: true
    reprocess_on_change: false
    include:
      - "*.csv"
    exclude:
      - "*.partial"
    settle_for: 3s

    pipeline:
      - name: process
        executor: command
        program: /usr/local/bin/process-file
        args:
          - "--input"
          - "{{file}}"
          - "--job-id"
          - "{{job_id}}"
          - "--shared-dir"
          - "{{shared_dir}}"
        timeout: 15m
        working_directory: "{{shared_dir}}"
        output: "{{shared_dir}}/{{stem}}.json"
        env:
          ONDERZEEER_INPUT: "{{basename}}"
```

Durations use Go syntax such as `250ms`, `10s`, `15m`, `2h`. The defaults are one
worker per CPU, a `10s` retry delay, a `1s` settle period, and `./onderzeeer.db`.
`max_retries` counts retries after the first attempt.

Relative database, watch, working-directory, and bind-mount source paths resolve
from the YAML file's directory after `values` expansion. Program paths containing
a slash resolve the same way; bare names use `PATH`. Absolute paths and paths
based on `{{file}}` or `{{dir}}` retain their meanings. Output paths resolve from
the command's working directory, or the runner's current directory if unset.

Structured Apptainer images follow the same rules for local paths and paths
after `docker-archive:` or `oci-archive:`. References containing `://` or starting
with `docker-daemon:` remain unchanged.

For standalone runs, use a distinct `database.path` per concurrent config. For
databases not yet created in the same directory, case-folded or Unicode-normalized
names are treated as aliases; pre-create them if your filesystem must distinguish
those names. Managed instances receive separate databases automatically.

Pipeline steps run sequentially. `executor` defaults to `command`, which requires
`program`. Other choices are `shell` (default `/bin/sh`), `docker`, `podman`, and
`apptainer` (their same-named CLIs on `PATH`). Set `program` to override the shell
or host-side container runtime binary.

Shell entries use `command` as shell source and support expansion, pipelines,
redirection, and other syntax provided by the selected shell:

```yaml
      - name: process-sidecars
        executor: shell
        command: |
          set -eu
          for file in "$1"/*.csv; do
            [ -e "$file" ] || continue
            process-file -- "$file"
          done
        command_args:
          - "{{dir}}"
```

The host invocation is exactly:

```text
/bin/sh -c <command> <step-name> <command_args...>
```

The step name is `$0`; `command_args` supplies `$1` onward and `"$@"`. Enable
shell options explicitly, as with `set -eu` above.

Config-local `values` may expand in shell source, but per-job templates such as
`{{file}}` are forbidden there. Pass job data through `command_args` or `env`
and quote the corresponding shell parameter to prevent filenames from becoming
executable shell source.

Container entries can describe the invocation with structured fields:

```yaml
      - name: process-in-container
        executor: docker
        image: ghcr.io/example/processor:1.2.3
        container_args:
          - "--rm"
          - "--network=none"
        mounts:
          - source: "{{dir}}"
            target: /input
            options:
              - ro
              - bind-propagation=rslave
        container_env:
          ONDERZEEER_INPUT: "{{basename}}"
        command: /app/process-file
        command_args:
          - "--input"
          - "/input/{{basename}}"
```

`image` is required. Each mount needs a host `source` and an absolute container
`target` after expansion. Its `options` are ordered, runtime-specific `--mount`
fields, passed through unchanged; do not repeat the mount type, source, or target
there. Templates work in options too. The legacy `read_only: true` alias becomes
a leading `ro` option.

`container_args` holds action options without `run` or `exec` itself. Use raw
`args` for runtime-global options that precede the action; a standalone `--` is
not allowed in structured mode. `command` is the optional first post-image token,
followed by `command_args`. If that first token starts with `-`, `parse` puts the
whole tail in `command_args` for the image's default entrypoint.

`container_env` sets container variables. Values appear in runtime arguments and
command history, so do not use it as a secret store. Mounts and Apptainer
environment values are CSV-encoded to preserve embedded commas and quotes.

For Docker and Podman, structured fields produce arguments in this order:

```text
run <container_args> <mount options> <environment options> <image> [<command>] <command_args>
```

Apptainer uses `exec` when `command` is set and `run` when it is omitted:

```text
exec|run --no-eval <container_args> <mount options> <environment options> <image> [<command>] <command_args>
```

Structured Apptainer invocations include `--no-eval` to disable startup
evaluation of environment values and OCI command tokens. Docker and Podman use
the image's normal `ENTRYPOINT` and `CMD` semantics. Add cleanup, networking,
working-directory, or environment-isolation options explicitly in
`container_args`, such as `--rm`, `--network=none`, or `--cleanenv`.

Use raw `args` for full control of the runtime invocation:

```yaml
        executor: docker
        args: ["run", "--rm", "example/image:latest", "process", "{{file}}"]
```

Raw `args` must include the subcommand, image, and runtime options, and cannot
be combined with structured container fields. Add `--no-eval` explicitly for
raw Apptainer invocations when needed.

Pipeline `env` and `working_directory` configure the host-side runtime process.
Use `container_env` for container variables and `container_args` for its working
directory. Apptainer inherits much of the host environment unless `--cleanenv`
is set. Use foreground runtime invocations: a step completes when the CLI exits.

Include and exclude patterns without a slash match the basename at any watched
depth. Patterns with a slash match the path relative to the watch root and may
use `**` for zero or more directories. Excludes take precedence. With
`reprocess_on_change: false`, a watch/path pair is processed once; when true, a
new size/modification-time fingerprint creates another persistent job.

Top-level `values` define reusable, config-local strings, as in `shared_dir`
above. Keys are case-sensitive identifiers (letters, digits, and underscores,
starting with a letter or underscore). Values can reference one another;
cycles and redefinitions of built-in template names are rejected. Undeclared
`{{name}}` placeholders remain unchanged for downstream tools.

Values expand before validation and path resolution; built-in templates within
them expand per job. Expanded values may appear in arguments, environment,
logs, and command history, so do not use them for secrets.

Per-job templates work in arguments, structured container images and commands,
mounts, working directories, output paths, and host or container environment
values. Shell source accepts only config-local `values`:

| Template | Value |
| --- | --- |
| `{{file}}` | Absolute file path |
| `{{dir}}` | Parent directory |
| `{{basename}}` | Filename with extension |
| `{{stem}}` | Filename without its final extension |
| `{{ext}}` | Final extension, including the dot |
| `{{job_id}}` | SQLite job ID |

Command and container executors pass arguments directly without a shell;
filename characters such as spaces, semicolons, and `$()` remain literal.
A container's entrypoint or runscript may still interpret its arguments.
Host-side `env` entries override the runner's environment for that command.

Stopping a Docker or Podman step kills the runtime CLI process group, but a
container managed by a separate daemon may outlive it. `--rm` removes a container
after exit; it does not stop it. Use a runtime-aware wrapper or container-side
deadline when cancellation must stop the workload.

Bind-mount sources must be accessible to the runtime; onderzeeer does not create
or transfer them. Remote daemons and VM-backed runtimes may not see host paths.

`output` saves complete stdout to a file; stderr stays in captured history.
The parent directory must exist. Each attempt creates or truncates the file, so
use job-specific paths for concurrent work and expect retries to replace partial
output. Captured stdout and stderr are each limited to their first 1 MiB per
command, followed by a truncation marker; this limit does not affect `output`.

Symlink files and directories are ignored. Watch only trusted directories:
another writer can replace a file between checking it and the command opening it.

Each instance supports up to 4,096 watched directories and 1,024 files settling
at once. Exceeding these limits fails the instance. Removing or replacing a watch
root, or a persistent loss of a kernel watch, also fails it; restore the expected
path and restart the instance.

### named shared resources

Declare resources at the top level and use the scalar `resources` field on each
step that needs exclusive access:

```yaml
resources:
  - gpu

watches:
  - name: incoming
    path: /srv/incoming
    pipeline:
      - name: prepare
        executor: command
        program: /opt/workflows/prepare
        args: ["{{file}}"]

      - name: test
        resources: gpu
        executor: docker
        image: example/processor:latest
        container_args: ["--rm", "--gpus", "all"]
```

The top-level list is optional. Each resource has capacity one, and a step may
reference at most one resource using a scalar string. Names are case-sensitive,
nonempty strings with no surrounding whitespace or template expansion. Non-string
values, duplicate declarations, undeclared references, and declarations unused
by any step in the configuration are rejected.

Reservations are **daemon-wide**: instances declaring `gpu` compete for the same
reservation while keeping separate queues and histories. Stopping one instance
does not affect reservations held by others.

A worker acquires the reservation immediately before executing the referencing
step and releases it when execution returns, including failure, timeout, or
cancellation. Requests are served in FIFO order. Waiting occupies a worker slot
and marks the job and its current run `PENDING`. They return to `RUNNING` after
the reservation is acquired. Waiting does not start the step timeout or spend
retries. Steps using different resources or no resources can run concurrently within each instance's worker
limit. Logs record waiting, acquisition, release, and canceled waits.

Canceling a waiting step removes its request and requeues the job. Its interrupted
run remains in history as failed but does not spend retries. Restarting replays
the whole pipeline; reservation order resets on daemon restart.

Standalone `onderzeeer test` coordinates resources only within that run. It does
not coordinate with managed instances or other standalone processes.

Workloads must stay in the foreground until all resource use finishes; avoid
detached containers (`-d`/`--detach`) and background work. The reservation covers
host-side execution only: a Docker container may outlive its CLI after
cancellation. Releasing a reservation does **not** prove the container stopped.
Exclusivity through cancellation requires verified container termination or
external resource coordination.

## delivery and recovery

SQLite persists jobs, retries, and execution history. On startup, unfinished
runs are marked interrupted and jobs left `RUNNING` or `PENDING` are immediately
requeued. Interrupted pending runs do not spend the execution retry budget.
Existing queue databases are upgraded automatically when opened for execution,
preserving job IDs and history.

Within each queue, workers claim eligible jobs by time added (`created_at`),
oldest first, with job ID breaking ties. Retry availability determines when a
job becomes eligible; it keeps its original time-added priority once eligible.
Concurrent workers may finish in a different order, and shared resource
reservations remain ordered by when each step requests them.

Execution is **at least once**: a command completed just before a crash may run
again after recovery, so pipelines should be idempotent. Stopping an instance
cancels active commands, records failed attempts when possible, and waits for
workers to exit. SIGINT and SIGTERM gracefully stop foreground runs; signaling
the daemon stops all its instances.

On Linux, cancellation kills the command's process group, including ordinary
descendants. Processes that create a new session or process group can escape;
output-pipe cleanup is time-bounded so they cannot indefinitely block shutdown.

## container

Run as your regular, non-root user from the Onderzeeer repository root.
The UID/GID must be nonzero and not conflict with existing base-image accounts.
For standard Docker without user-namespace remapping:

```bash
docker build --pull \
  --build-arg ONDERZEEER_UID="$(id -u)" \
  --build-arg ONDERZEEER_GID="$(id -g)" \
  --build-arg VERSION="$(git describe --tags --always --dirty)" \
  -t localhost/onderzeeer:local .
```

For Podman, replace `docker build --pull` with
`podman build --pull=always --format docker` to retain the image health check.
For reproducible base images, override `NODE_IMAGE`, `GO_IMAGE`, and
`RUNTIME_IMAGE` with digest-pinned references. Builds target the selected image
platform; cross-compilation or emulation requires separate setup.

### prepare the socket and workspace

Use a private socket directory and a persistent workspace:

```bash
SOCKET_DIR="${XDG_RUNTIME_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}}/onderzeeer"
WORKSPACE="$HOME/onderzeeer-work"

install -d -m 0700 "$SOCKET_DIR"
mkdir -p "$WORKSPACE/configs" "$WORKSPACE/incoming" "$WORKSPACE/state"
```

Do not run another daemon against this socket. Existing socket/lock files must
belong to the same user. The mounted directory must be owned by the daemon's
mapped UID and have mode 0700; image-layer ownership cannot fix a bind mount.

### run with Docker

```bash
docker run -d \
  --name onderzeeerd \
  --restart unless-stopped \
  --stop-timeout 45 \
  --mount "type=bind,src=$SOCKET_DIR,dst=/run/onderzeeer" \
  --mount "type=bind,src=$WORKSPACE,dst=$WORKSPACE" \
  --workdir "$WORKSPACE" \
  --env "ONDERZEEER_STATE_DIR=$WORKSPACE/state" \
  localhost/onderzeeer:local
```

### run with rootless Podman instead

```bash
podman run -d \
  --name onderzeeerd \
  --restart unless-stopped \
  --stop-timeout 45 \
  --userns=keep-id \
  --mount "type=bind,src=$SOCKET_DIR,dst=/run/onderzeeer" \
  --mount "type=bind,src=$WORKSPACE,dst=$WORKSPACE" \
  --workdir "$WORKSPACE" \
  --env "ONDERZEEER_STATE_DIR=$WORKSPACE/state" \
  localhost/onderzeeer:local
```

On SELinux-enforcing systems, replace the two `--mount` options with:

```bash
-v "$SOCKET_DIR:/run/onderzeeer:z" \
-v "$WORKSPACE:$WORKSPACE:z"
```

Only relabel these dedicated directories, not your whole home/runtime directory.
The shared `:z` label is appropriate when multiple containers share the mounts.
SELinux socket-connection policy can require additional configuration even after
filesystem permissions and labels are correct. Do not globally disable SELinux
as a workaround. Rootless restart-at-boot requires separate service/session setup.

### connect a host-side client

```bash
export ONDERZEEER_SOCKET="$SOCKET_DIR/onderzeeer.sock"
onderzeeer ps

# Once this config exists and its paths/executables are usable in the container:
onderzeeer start "$WORKSPACE/configs/incoming.yaml" incoming

# The image includes a client for diagnostics too:
docker exec onderzeeerd onderzeeer ps
docker logs onderzeeerd
```

Use `podman exec` and `podman logs` for a Podman-managed container.

The workspace is mounted at the same absolute path on both sides because
`onderzeeer start` sends config paths to the daemon, which reads and saves a snapshot.
Set `ONDERZEEER_STATE_DIR` to a persistent mounted directory; the Compose example
uses `${ONDERZEEER_WORKSPACE}/state`. It contains both the registry and managed queues.
Persist the complete state directory, including SQLite companion files.

The health check validates control-API connectivity, not successful pipeline
processing. Override the socket through `ONDERZEEER_SOCKET` rather than only through
`--socket`, so both daemon and health-check client use the new address.

### restart behavior

Instances submitted with `onderzeeer start` resume automatically when the container
starts using the same persistent `ONDERZEEER_STATE_DIR`. Explicitly stopped
instances stay stopped. Register new instances through `onderzeeer start` after
the container is running.

### optional dashboard

To enable the embedded dashboard, add these options **before** the image name:

```bash
--env ONDERZEEER_WEB_LISTEN=0.0.0.0:5280 \
--publish 127.0.0.1:5280:5280
```

Open <http://127.0.0.1:5280> and read the access token on the host:

```bash
# Docker
docker exec onderzeeerd cat /run/onderzeeer/onderzeeer.sock.web-token

# Docker Compose
docker compose exec -T onderzeeerd cat /run/onderzeeer/onderzeeer.sock.web-token
```

This binds the application to all interfaces inside the container but publishes
its port only on the host loopback address. Other containers with network access
to this container may still reach it. Keep the bearer token private, and do not
expose its unencrypted HTTP listener to an untrusted network.

## systemd

Install the binaries and [example unit](contrib/systemd/onderzeeer.service),
create a dedicated service account, and add a config:

```bash
go build -o onderzeeer ./cmd/onderzeeer
go build -o onderzeeerd ./cmd/onderzeeerd
sudo install -Dm0755 onderzeeer /usr/local/bin/onderzeeer
sudo install -Dm0755 onderzeeerd /usr/local/bin/onderzeeerd
sudo install -Dm0644 contrib/systemd/onderzeeer.service /etc/systemd/system/onderzeeer.service

# Skip useradd if the account already exists.
sudo useradd --system --home-dir /var/lib/onderzeeer --shell /usr/sbin/nologin onderzeeer
sudo install -d -m0755 /etc/onderzeeer.d
sudo install -o root -g onderzeeer -m0640 ./my-onderzeeer.yaml /etc/onderzeeer.d/incoming.yaml
```

Replace `./my-onderzeeer.yaml` with your [configuration](#configuration).

The system service sets `ONDERZEEER_STATE_DIR=/var/lib/onderzeeer` for the registry and
managed queues. Every managed pipeline runs as the `onderzeeer` account,
so ensure that account can traverse each watch directory, read input files,
execute pipeline programs, and write any pipeline outputs. systemd creates
`/var/lib/onderzeeer` through `StateDirectory=onderzeeer` and the private socket directory
`/run/onderzeeer` through `RuntimeDirectory=onderzeeer`.

Set the control socket and optional dashboard in `/etc/default/onderzeeer`:

```bash
ONDERZEEER_SOCKET=/run/onderzeeer/onderzeeer.sock
# Optional; the dashboard is disabled when this is unset.
# ONDERZEEER_WEB_LISTEN=127.0.0.1:8080
# Optional public viewing; start/stop still requires a token.
# ONDERZEEER_WEB_PUBLIC_READ=true
```

The unit restores registered instances and restarts after failures. Register
instances with `onderzeeer start` after starting the service; no unit edits are
needed for individual instances.

Load and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now onderzeeer
sudo systemctl status onderzeeer
sudo onderzeeer ps --socket /run/onderzeeer/onderzeeer.sock
sudo journalctl -u onderzeeer -f
```

Apply later changes with `sudo systemctl restart onderzeeer`.

The packaged system socket is private to root and the `onderzeeer` service account.
If you deliberately relax its ownership or permissions, every user who can
connect to it can make the service execute configured programs as `onderzeeer`. A
per-user daemon is recommended for interactive and user-owned workloads.

## architecture

- `internal/config`: discovery, strict YAML decoding, defaults, and validation
- `internal/watcher`: Linux filesystem events, recursive discovery, matching,
  and settling
- `internal/queue`: SQLite jobs, runs, command history, claims, and recovery
- `internal/executor`: backend interface and safe local process executor
- `internal/worker`: concurrent consumers and sequential pipeline execution
- `internal/daemon`: one instance's component lifecycle and discovery-to-queue
  wiring
- `internal/control`: instance supervision, Unix-socket API, and client
- `internal/webui`: optional token-protected dashboard API and embedded frontend
- `internal/cli`: command parsing for the `onderzeeer` client and `onderzeeerd` daemon

## development

Install [Go](https://go.dev/dl/) at the version required by `go.mod` or newer.
For a repository-local toolchain, place Go at `.go/toolchain/bin/go` and run
`source activate` to select it and keep Go's caches under `.go`.

### isolated temporary Go toolchain

If Go is not installed, or you want to keep the toolchain and its caches out of
your home directory, you can download Go into a temporary directory. This
example uses Go 1.27.0 for Linux on AMD64; choose another published version or
supported Linux architecture from [go.dev/dl](https://go.dev/dl/) when needed.

```bash
export ONDERZEEER_GO_VERSION=1.27.0
export ONDERZEEER_GO_OS=linux
export ONDERZEEER_GO_ARCH=amd64
export ONDERZEEER_GO_DIR=$(pwd)/.go

mkdir -p "$ONDERZEEER_GO_DIR/toolchain"
curl -fL \
  "https://go.dev/dl/go${ONDERZEEER_GO_VERSION}.${ONDERZEEER_GO_OS}-${ONDERZEEER_GO_ARCH}.tar.gz" \
  -o "$ONDERZEEER_GO_DIR/go.tar.gz"
tar -xzf "$ONDERZEEER_GO_DIR/go.tar.gz" \
  -C "$ONDERZEEER_GO_DIR/toolchain" \
  --strip-components=1

export PATH="$ONDERZEEER_GO_DIR/toolchain/bin:$PATH"
export GOPATH="$ONDERZEEER_GO_DIR/gopath"
export GOMODCACHE="$ONDERZEEER_GO_DIR/modcache"
export GOCACHE="$ONDERZEEER_GO_DIR/buildcache"

go version
go mod download
```

`PATH` selects the temporary Go binary, `GOPATH` isolates Go's workspace,
`GOMODCACHE` isolates downloaded modules, and `GOCACHE` isolates compiled build
artifacts. You do not need to set `GOROOT`; the Go binary discovers its own
toolchain directory. These exports affect only the current shell.

For Linux on ARM64, set `ONDERZEEER_GO_ARCH=arm64`.

To remove the temporary toolchain and caches when you are finished:

```bash
if [ -n "${ONDERZEEER_GO_DIR:-}" ] && [ -d "$ONDERZEEER_GO_DIR" ]; then
  rm -rf -- "$ONDERZEEER_GO_DIR"
fi
unset ONDERZEEER_GO_DIR ONDERZEEER_GO_VERSION ONDERZEEER_GO_OS ONDERZEEER_GO_ARCH
unset GOPATH GOMODCACHE GOCACHE
hash -r
```

### checks

```bash
go fmt ./...
go vet ./...
go test ./...
```

The production dashboard assets are committed beneath `internal/webui/dist`
so normal Go builds need no Node.js installation. After changing files under
`web`, rebuild those assets with Node.js 22:

```bash
(cd web && npm ci && npm run build)
```

Or run `make web` to rebuild through Docker.

### build

```bash
make
./bin/onderzeeer version
./bin/onderzeeerd version
```

![icon](icon.png)
