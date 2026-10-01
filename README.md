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

This release provides schedule management: CRUD, pause/resume, owner email filters,
cursor pagination, cron preview and additional `env_from` names. Execution, run
history, SAML, the CLI and the web interface follow in separate implementation
stages. Creating an active schedule in this release does **not** run an agent.

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
The API never calls the core in this release and needs no core credentials.

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

## Configuration and authentication

| Environment | Purpose |
| --- | --- |
| `DATABASE_URL` | Space's PostgreSQL DSN; required |
| `PUBLIC_API_KEYS` | JSON array of Space Bearer keys |
| `ORPHEUS_BROWSER_AUTH` | `api_only` (default) or explicit local `anonymous` |
| `ORPHEUS_PUBLIC_URL` | Exact HTTP(S) origin; required for anonymous browser writes |
| `ORPHEUS_CONFIG_FILE` | Base execution TOML, default `orpheus-space.toml` |
| `ORPHEUS_MIGRATIONS_DIR` | Goose files, default `migrations` |
| `HARNESS_ENV_ALLOWLIST` | JSON array of permitted ENV names |
| `ORPHEUS_HOST`, `ORPHEUS_PORT` | API bind, defaults `0.0.0.0:8000` |
| `ORPHEUS_SYSTEM_HOST`, `ORPHEUS_SYSTEM_PORT` | System bind, defaults `0.0.0.0:9100` |
| `MAX_REQUEST_BYTES` | Body limit, default 1048576, range 4096–1048576 |

A valid Bearer key grants read/write access. Any supplied invalid or empty
Authorization header returns 401, without anonymous fallback. Without Bearer,
api_only returns 401; anonymous permits reads and requires the exact configured
Origin and `X-Orpheus-CSRF: 1` on POST/PATCH/DELETE. Cookies and client email headers
do not confer access. SAML is not enabled yet; unknown/unsupported modes fail startup.

The TOML configuration follows core's agent/sandbox/limits structure; see
[orpheus-space.toml.dist](orpheus-space.toml.dist). `instructions_file` and inline
`instructions` are mutually exclusive. The API stores only extra ENV names, never
values. Both base names and additions must be allowlisted; duplicates are rejected.
When execution is added, the core worker will resolve values for the union of base
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
