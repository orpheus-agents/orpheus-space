# Profile and template selection

Schedules store concrete `profile` and `template` names. Creation uses
`execution.agent.profile` and `execution.sandbox.template` when the corresponding
field is omitted. Changing these defaults affects only newly created schedules.
PATCH changes only supplied fields; null, empty and unknown names return 422 with
the field in `error.details[].path`.

`GET /api/v1/schedules/profiles` and `/templates` return `{items: [...]}` with
Orpheus's public configuration, descriptions and `is_default`. A missing configured
default leaves all flags false. Requests use the service's Orpheus key; browser
cookies are never forwarded. Upstream failures return 503 `core_unavailable`.

Creation and changed selections require the relevant catalogs. Reading a task,
pausing it or editing its other fields does not. Removed selections remain stored;
there is no automatic replacement. Retrying a creation with the same idempotency
key and normalized input returns its saved result without catalog access, even
when defaults change. Omitted fields and explicitly supplied names are distinct
inputs for idempotency.

A profile or template change starts a new reusable session on the next unprepared
occurrence. Already prepared requests keep their original body, path and key.
Worker uses the stored names without looking up catalogs; Orpheus validates new
sessions. A rejection preserves `unknown_profile` or `unknown_template` in history.
Descriptions do not affect execution. The optional model overrides the profile's
model. Execution instructions override profile instructions only when configured;
base ENV and limits continue to come from Space settings.

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
