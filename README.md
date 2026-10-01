<p align="center">
  <a href="https://orpheus-agents.github.io/">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset=".github/orpheus-logo.svg">
      <img src=".github/orpheus-logo-light.svg" alt="Orpheus" width="240">
    </picture>
  </a>
</p>

# Orpheus Space

Shared settings for Orpheus agent users. Go 1.27, PostgreSQL 16.

Space manages and executes schedules through the Orpheus core. The API and worker
are separate commands using the same database and image. SAML, CLI and the web
interface follow in separate implementation stages.

## Local development

Requirements: Docker, Docker Compose v2.24 or newer, Make.

```sh
cp .env.dist .env
cp orpheus-space.toml.dist orpheus-space.toml
make start
curl -fsS http://localhost:9110/ready
curl -fsS -H 'Authorization: Bearer local-space-key' http://localhost:8010/api/v1/schedules
```

`make stop` preserves database volumes. The API listens on local port 8010 and
system probes on 9110; container defaults match core (8000/9100). Compose supplies
`.env`; the binary does not load dotenv files. Space uses its own database DSN.
Set `ORPHEUS_BASE_URL` and `ORPHEUS_API_KEY` in `.env` to connect a core instance
reachable from Docker. `make start-worker` starts the planner separately;
`make start` runs only the API/database. The worker requires core credentials;
the API can run without them, returning 503 from the explicit result endpoint.
Generated core client version: v0.4.0. No AgentBox credentials are needed here.

## API

The contract lives in [api/openapi.yaml](api/openapi.yaml), served as
`GET /openapi.json`. Generated Go server types and the public [Go client](client/README.md)
are checked into this repository. Import `github.com/orpheus-agents/orpheus-space/client`
and pin a release tag of the root module.

| Endpoint | Behavior |
| --- | --- |
| `GET/POST /api/v1/schedules` | List/create schedules |
| `GET/PATCH/DELETE /api/v1/schedules/{id}` | Read/edit/soft delete |
| `GET /api/v1/schedules/settings` | Base and allowed ENV names, auth mode |
| `POST /api/v1/schedules/preview` | Five future UTC times for cron/timezone |
| `GET /api/v1/schedules/{id}/occurrences` | Stored history, cursor pagination |
| `GET /api/v1/schedules/{id}/occurrences/{occurrence_id}` | Stored status/error and observation time |
| `GET /api/v1/schedules/{id}/occurrences/{occurrence_id}/result` | Explicit core read of current run status, final message and error |
| `POST /api/v1/schedules/{id}/reset-session` | Detach reusable session; 409 while an occurrence is active |
| `GET /api/v1/auth/session` | Public browser access state |

All users with access can edit all schedules. `owner_email` is an editable filter,
not an authorization boundary. Missing owner means a shared schedule. Repeated
`owner_email` filters use OR semantics; `unowned=true` selects shared schedules
and cannot be combined with email filters. Email addresses are trimmed/lowercased;
provider-specific aliases are not merged. List order is `(created_at DESC, id DESC)`.
Pass `next_cursor` back as `cursor` with the same filters. New inserts do not enter
an ongoing traversal; edits and deletions reflect current data.

Create requires name, prompt, five-field cron and IANA timezone. Defaults are
`status=active`, `session_mode=new`, `model=null`, `owner_email=null`, `env_from=[]`.
Macros, seconds, years and inline TZ are rejected. `Local` is not a timezone input.
DST follows cron wall-clock semantics: missing times are skipped, repeated times
occur twice. Preview uses the same parser as persistence.

Send a UUID `Idempotency-Key` when creating: retries of the same normalized input
return the original response, including after later edits/deletion; a different
input returns 409. Keys do not expire. PATCH changes only supplied fields; null
clears model/owner, while `env_from: []` clears additions. Pause removes the next
run time. Resume or cron/timezone changes calculate a new future time. Other edits
preserve the planned time. DELETE hides a schedule from lists but keeps its card
with `deleted_at`; repeated DELETE returns 204.

Errors use `error: {code, message, phase, details}`. Validation details identify
the location and field, for example `["body", "name"]` or `["query", "limit"]`,
with codes such as `required`, `invalid_type`, `unknown_field` and `invalid_value`.
API responses are no-store.

## Execution and recovery

The worker holds a PostgreSQL advisory lock on a dedicated connection; a second
worker fails startup. Connection loss cancels requests and stops planning. Run one
replica with Recreate. Eight concurrent requests, HTTP timeouts, and bounded
backoff isolate unavailable schedules. System probes run on the worker's own
9100 listener, separate from the API container.

After downtime, only the latest due period is considered. Periods during a
previous run are skipped using its actual `finished_at`; unknown outcomes block
new execution until reconciled. There is no catch-up queue. `accepted` means the
core accepted the request; `run_status` records execution separately. Read errors
retain the last status and `observed_at`, setting `sync_error_code`.

This includes core 404 (`run_not_found` / `session_not_found`): Space keeps polling,
blocks further executions and returns 409 on session reset. A missing run does not
prove completion; for example, a wrong core URL may hide a still-running task.
Check `ORPHEUS_BASE_URL` and restore access to the original core/run. Once its
terminal status is observed, planning resumes using its actual `finished_at`.
If the core data is permanently lost, operator investigation is required; there
is no automatic failure or force-reset API.

Agent message front matter renders `scheduled_at` and `dispatched_at` in the
schedule's timezone with the applicable UTC offset. Metadata retains UTC times.

Before dispatch, Space persists the exact request bytes, path and idempotency key.
Retries and restarts reuse them. Pause/delete cancels pending work; dispatching
work is reconciled even after pause/delete, without cancelling an accepted run.
An uncertain outcome stays unresolved on later auth/replay errors. Agent failures
are not retried automatically. None of the snapshots contain secret ENV values.

`new` creates single-run sessions. `reuse` continues the same session until model,
ENV names, session mode or effective base configuration changes, or an explicit
reset. Prompt/name/owner/cron edits preserve session history. Old sessions are not
deleted by Space. Capacity and session-busy errors retry the same request;
idempotency conflicts block that occurrence for operator investigation.

Lists, cards, history, settings and reset use only Space's database. Only `result`
reads the core, never writing the returned text/status back to the local history.
Missing runs return 404, not-started occurrences 409, and core auth/network failures
503. The core remains the only archive of messages and full results.

## Configuration and authentication

| Environment | Purpose |
| --- | --- |
| `ORPHEUS_BASE_URL`, `ORPHEUS_API_KEY` | Core origin/key for dispatch, polling and explicit results |
| `DATABASE_URL` | Space's PostgreSQL DSN; required |
| `PUBLIC_API_KEYS` | JSON array of Space Bearer keys |
| `ORPHEUS_BROWSER_AUTH` | `api_only` (default) or explicit local `anonymous` |
| `ORPHEUS_PUBLIC_URL` | Exact HTTP(S) origin; required for anonymous browser writes |
| `ORPHEUS_CONFIG_FILE` | Base execution TOML, default `orpheus-space.toml` |
| `ORPHEUS_MIGRATIONS_DIR` | Goose files, default `migrations` |
| `HARNESS_ENV_ALLOWLIST` | JSON array of permitted ENV names |
| `ORPHEUS_HOST`, `ORPHEUS_PORT` | API bind, defaults `0.0.0.0:8000` |
| `ORPHEUS_SYSTEM_HOST`, `ORPHEUS_SYSTEM_PORT` | System bind, defaults `0.0.0.0:9100` |
| `WORKER_POLL_SECONDS` | Positive polling interval in seconds, default 1; fractions supported. Lock monitoring stays at 1 second |
| `MAX_REQUEST_BYTES` | Body limit, default 1048576, range 4096–1048576 |

A valid Bearer key grants read/write access. Any supplied invalid or empty
Authorization header returns 401, without anonymous fallback. Without Bearer,
api_only returns 401; anonymous permits reads and requires the exact configured
Origin and `X-Orpheus-CSRF: 1` on POST/PATCH/DELETE. Cookies and client email headers
do not confer access. SAML is not enabled yet; unknown/unsupported modes fail startup.

The TOML configuration follows core's agent/sandbox/limits structure; see
[orpheus-space.toml.dist](orpheus-space.toml.dist). `instructions_file` and inline
`instructions` are mutually exclusive. Omitting both preserves core profile
instructions; an explicit empty string clears them. The API stores only extra ENV names, never
values. Both base names and additions must be allowlisted; duplicates are rejected.
The core worker resolves values for the union of base
and task names. Do not pass database or service-auth secrets to agent environments.

## Development checks

All tools run in containers; host Go/Python is unnecessary.

| Command | Purpose |
| --- | --- |
| `make tools` / `tools-build` | Ensure/rebuild pinned tools image |
| `make start` / `stop` / `migrate` | Local API and PostgreSQL lifecycle |
| `make generate` / `generate-check` | OpenAPI server/client and sqlc generation/check |
| `make generate-client` / `generate-client-check` | Client-only generation/check |
| `make sqlc-check` | Prepare queries against an isolated Goose schema |
| `make fix` / `gofix` | Format and apply Go fixes |
| `make test` / `test-go-race` | Unit, PostgreSQL, migration and race tests |
| `make test-client` / `build-client` | Public client checks |
| `make lint` / `deadcode` / `vuln` | API/Go/Docker lint, dead code, vulnerability scans |
| `make build` / `docker-build` / `smoke` | Compile/build and test production image |
| `make check` | Full applicable suite |

Tests use isolated schemas on `test-db` and exercise migrations Up/Down/Up.
`/health` checks the process; `/ready` requires all migrations shipped with the
service to be applied. System routes are not mounted on the public API listener.
SIGTERM drains HTTP requests. Unexpected API failures log the method, route pattern, innermost error type and,
for PostgreSQL errors, SQLSTATE without error text or request values. Cancelled
requests are not logged as failures. Invalid TOML
reports its line and column without including configuration values.

## CI and release

PR and main CI run `make check` and `make smoke`. After review and green PR CI,
merge and tag the merge commit without waiting for repeated main CI. Version tags
publish `retailcrm/orpheus-space` for linux/amd64 and linux/arm64 using
`vars.DOCKERHUB_USERNAME` and `secrets.DOCKERHUB_TOKEN`. Public client source shares
the same module tag. CLI binaries and skill assets will be added with those features.
