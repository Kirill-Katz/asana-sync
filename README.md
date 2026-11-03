# Asana Extractor

Periodically fetches projects and users from an Asana workspace and saves each record as a JSON file.

## Build and run

Use the Go toolchain specified in `go.mod` (Go 1.27.3). From the repository root, build the executable:

```bash
go build -o extractor ./cmd/extractor
```

Create a `.env` file in your working directory:

```dotenv
API_TOKEN=your-asana-token
WORKSPACE_ID=your-workspace-gid
API_HOST=https://app.asana.com/api/1.0
```

All three settings are required. Environment variables override values in `.env`. `API_HOST` is the full API base URL, including `/api/1.0`. Keep the token and `.env` file private.

Run with the default settings:

```bash
./extractor
```

Or specify the output directory, polling intervals, and retry settings:

```bash
./extractor \
  --output-dir ./data \
  --projects-interval 30s \
  --users-interval 1m \
  --max-attempts 5 \
  --max-backoff 30s \
  --max-jitter 1s \
  --debug
```

You can also run without building first:

```bash
go run ./cmd/extractor --output-dir ./data --debug
```

Both synchronization jobs run immediately at startup, then on their respective intervals. Press **Ctrl+C** to cancel polling and wait for in-flight operations to finish. Logs use space-separated `slog` text output on stderr, not JSON.

Records are stored as:

```text
data/
├── projects/
│   └── project_<gid>.json
└── users/
    └── user_<gid>.json
```

## CLI parameters

| Parameter | Description | Default |
| --- | --- | --- |
| `--output-dir` | Root directory for the `projects/` and `users/` JSON subdirectories. Missing directories are created automatically. | `./data` |
| `--projects-interval` | Interval between project synchronization ticks. Must be positive. | `1m` |
| `--users-interval` | Interval between user synchronization ticks. Must be positive. | `1m` |
| `--max-attempts` | Maximum request attempts per page, including the initial attempt. Must be at least 1. | `5` |
| `--max-backoff` | Maximum exponential retry backoff before adding jitter. Must be at least `1s`. | `30s` |
| `--max-jitter` | Maximum additive random retry jitter, sampled from `[0, max-jitter]`. Must be non-negative. | `1s` |
| `--debug` | Enable debug logs for fetched pages. Info and error logs are enabled by default. | `false` |
| `--help` | Print CLI usage and exit. | — |

Duration values use Go duration syntax, such as `500ms`, `10s`, or `5m`.

## Architecture

All application components live in `internal/`; `cmd/extractor/main.go` loads configuration and wires them together.

- **API client — `AsanaAPIClient`:** Handles authenticated HTTP requests, workspace-scoped pagination, and bounded retries. All attempts, including retries, share a token bucket with capacity 2, replenished with 2 tokens every 2 seconds. Transient failures use `Retry-After` seconds when available, otherwise exponential backoff plus jitter. Redirects are disabled on the HTTP client configured by the CLI.
- **Store — `AsanaStorer` / `AsanaJsonStore`:** Persists individual projects and users as JSON files. Each save writes to a temporary file in the destination subdirectory, closes it successfully, then renames it to the final filename. This prevents readers from seeing partial records and keeps existing files intact until replacement is ready.
- **Integration — `AsanaIntegration`:** Uses the API client to fetch projects or users, then saves each record through the `AsanaStorer` interface. `SyncProjects` and `SyncUsers` propagate context and stop on cancellation or the first error.
- **Poller — `Poller` and `Job`:** Receives a list of jobs, each with a name, interval, and polling callback. Each job has its own goroutine and ticker. Polling runs synchronously within that worker, preventing a job from overlapping with itself while allowing different jobs to run concurrently. Errors are reported without stopping future polls.

```text
Poller → Job.Poll → AsanaIntegration → AsanaAPIClient
                                   → AsanaStorer → JSON files
```
