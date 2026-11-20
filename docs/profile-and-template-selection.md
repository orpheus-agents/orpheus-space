# Profile and template selection

Space supports Codex and OpenCode profiles from the same Orpheus catalog. A schedule selects a profile by name and uses its configured harness, provider and model.

Schedules store concrete `profile` and `template` names. Creation uses
`execution.agent.profile` and `execution.sandbox.template` when the corresponding
field is omitted. Changing these defaults affects only newly created schedules.
PATCH changes only supplied fields; null, empty and unknown names return 422 with
the field in `error.details[].path`.

`GET /api/v1/profiles` and `GET /api/v1/templates` each return one page of the
core database catalog, including inactive entries. Both expose public descriptions
and `is_default`: name equality with the configured default, not availability.
Requests use Space's Orpheus key; browser cookies are never forwarded.

Both lists accept `active`, exact case-sensitive `name`, literal case-insensitive
`name_contains`, `limit` (1–200, default 50), and `cursor`. Profiles additionally
accept `available`, which means both the profile and its account are active.
The two name filters cannot be combined; blank or NUL-containing searches fail.
Responses preserve `next_cursor` and `prev_cursor` without local repagination.
Query errors retain 422, including `invalid_cursor`, with safe `query.*` details.
Other upstream failures return 503 `core_unavailable`.

`GET /api/v1/schedules/settings` includes `default_profile` and `default_template`,
independent of catalog pagination. Resolve a default or saved selection through
an exact `name` query without activity/availability filters; it may be absent or
inactive. Do not infer absence from search results. Only automatically select
an available default; never substitute the first search result.

Creation (including paused tasks), all edits and resume require an existing active
template and an available profile, even if the patch omits their names or repeats
them. Profile errors identify `body.profile` with `profile_not_found`,
`profile_inactive` or `account_inactive`; template errors identify `body.template`
with `template_not_found` or `template_inactive`.
Only a patch containing exactly `{"status":"paused"}` skips catalog validation.
Pause and delete work while core is unavailable. Additional fields, even unchanged
values, make a pause patch an ordinary edit.

Removed/inactive selections remain stored; there is no automatic replacement.
Retrying an accepted creation with the same idempotency key and normalized input
returns its saved result without catalog access, even when defaults change.
Omitted fields and explicitly supplied names are distinct idempotency inputs.

A profile or template change starts a new reusable session on the next unprepared
occurrence. Already prepared requests keep their original body, path and key.
Worker uses the stored names without looking up catalogs. Orpheus validates new
sessions against exact catalog names, active profiles/accounts and active templates. Changes to
the catalog do not affect accepted sessions or idempotent replays. A rejection preserves the core error code in history.
Reassigning a profile to another account does not reset a reusable session: it
keeps its pinned account. Use the existing session reset to apply the new account.
Manual run requests use the same queue and reuse behavior; the API does not
precheck catalogs for this action.
Descriptions do not affect execution. The optional model overrides the profile's
model. Execution instructions override profile instructions only when configured;
limits continue to come from Space settings. Explicit service codes are stored
on the schedule. `GET /api/v1/services` exposes the complete service catalog.
A selection change starts fresh context. Reset context to apply a service
definition changed under the same code. No service is selected by default.

## Existing databases

Stop old API and worker processes before upgrading. Run `migrate up` with the
current TOML mounted at `ORPHEUS_CONFIG_FILE`, then start the new processes.
Migrations 5–7 add the columns, fill data, then require nonempty names.
The Go data migration reads only the two defaults, including for paused/deleted
schedules and saved creation responses; it does not read instruction or SAML
files or call Orpheus. Existing values, occurrence requests and idempotency
fingerprints are preserved. Missing defaults fail the data migration.

An empty database and readiness checks need no TOML. Reapplying the data migration
does not overwrite populated fields. Rolling back the schema drops the new
columns; the additive fields in saved creation responses are retained.
