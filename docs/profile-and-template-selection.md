# Profile and template selection

Schedules store concrete `profile` and `template` names. Creation uses
`execution.agent.profile` and `execution.sandbox.template` when the corresponding
field is omitted. Changing these defaults affects only newly created schedules.
PATCH changes only supplied fields; null, empty and unknown names return 422 with
the field in `error.details[].path`.

`GET /api/v1/profiles` returns the full profile catalog. `GET /api/v1/templates`
returns one page of the core database catalog, including inactive entries. Both
expose public descriptions and `is_default`; for templates this flag only means
name equality with the configured default, not activity. Requests use Space's
Orpheus key; browser cookies are never forwarded.

Template list parameters match core: `active`, exact case-sensitive `name`,
literal case-insensitive `name_contains`, `limit` (1–200, default 50), and `cursor`.
The two name filters cannot be combined; blank or NUL-containing searches fail.
Responses preserve `next_cursor` and `prev_cursor` without local repagination.
Query errors retain 422, including `invalid_cursor`, with safe `query.*` details.
Other upstream failures return 503 `core_unavailable`.

`GET /api/v1/schedules/settings` includes `default_template`. Resolve it or a
saved selection through `GET /api/v1/templates?name=<encoded-name>` without an
active filter; it may be absent or inactive. Do not infer absence from a page
of search results. A default outside the first page still remains the default.

Creation (including paused tasks), all edits and resume require the effective
template to exist and be active, even if the patch omits `template` or repeats
the saved name. Errors identify `body.template` with `template_not_found` or
`template_inactive`. Only a patch containing exactly `{"status":"paused"}`
skips catalog validation. Pause and delete work while core is unavailable.
Additional fields, even unchanged values, make a pause patch an ordinary edit.
Profile validation still applies only to new or changed profile choices.

Removed/inactive selections remain stored; there is no automatic replacement.
Retrying an accepted creation with the same idempotency key and normalized input
returns its saved result without catalog access, even when defaults change.
Omitted fields and explicitly supplied names are distinct idempotency inputs.

During the template catalog rollout, core's admission still reads TOML. A newly
registered catalog name can be selected in Space yet rejected by core until
TOML contains it. The separate runtime switch removes that transition period.

A profile or template change starts a new reusable session on the next unprepared
occurrence. Already prepared requests keep their original body, path and key.
Worker uses the stored names without looking up catalogs; Orpheus validates new
sessions. A rejection preserves `unknown_profile` or `unknown_template` in history.
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
