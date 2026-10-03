# CLI

`ORPHEUS_SPACE_HOST` is the origin without `/api/v1`; `ORPHEUS_SPACE_API_KEY` is
the Space key. Never pass the key as an argument or print it. `--host` overrides
only the endpoint. `--help` and `--version` work without API access.

```sh
orpheus-space schedule list --owner-email alice@example.com --limit 50 --json
orpheus-space schedule list --owner-email alice@example.com --cursor '<next_cursor>' --json
orpheus-space schedule get '<id>' --json
orpheus-space profiles --json
orpheus-space templates --json
orpheus-space services --json
orpheus-space schedule settings --json
orpheus-space schedule preview --cron '0 10 * * 1-5' --timezone Europe/Moscow --json
orpheus-space schedule create --file schedule.json --json
orpheus-space schedule update '<id>' --file patch.json --json
orpheus-space schedule pause '<id>' --json
orpheus-space schedule resume '<id>' --json
orpheus-space schedule history '<id>' --limit 50 --json
orpheus-space schedule occurrence '<id>' '<occurrence-id>' --json
orpheus-space schedule result '<id>' '<occurrence-id>' --json
orpheus-space schedule reset-session '<id>' --json
orpheus-space schedule delete '<id>' --json
```

Create input (`schedule.json`):

```json
{
  "name": "Morning report",
  "prompt": "Prepare a report using the agreed sources and result destination.",
  "cron": "0 10 * * 1-5",
  "timezone": "Europe/Moscow",
  "owner_email": "alice@example.com",
  "session_mode": "new",
  "services": []
}
```

Creation accepts optional `profile` and `template` names from the catalogs;
omitted fields save the installation's defaults. A patch such as
`{"profile":"<catalog-name>","template":"<catalog-name>"}` changes the selection.
Neither field accepts null or an empty string. Read the saved names in the returned
schedule. Selection changes start a new reusable session at the next unprepared run.

A patch contains only fields to change. `{"model":null,"services":[]}` removes
the model override and selected services. `--file -` reads JSON from stdin.
Use a JSON serializer for multiline prompts; do not construct shell commands
by interpolating user text.

Create uses a UUID Idempotency-Key. You can supply `--idempotency-key <uuid>`
beforehand. After a request error, the CLI reports the key to reuse: retry
**the same JSON with the same key**. Do not generate a new key after a network
failure. The CLI does not retry automatically.

list/get/history/occurrence read stored Space data. `profiles`, `templates`, `services` and
`result` explicitly query Orpheus and may fail when it is unavailable. List/history return one page and
next_cursor; they do not fetch all pages automatically. Repeated `--owner-email`
filters use OR semantics; when acting for a user, select only the request author.
`--unowned` is available for administrative use and is outside this skill's
owner-scoped conversational workflow.

Schedule objects returned by get, list, create, update, pause, resume and
reset-session include `url`: the absolute web card link, or null if the server
has no public URL configured. Use this field rather than the API endpoint.

stdout contains JSON (compact with `--json`, indented otherwise); stderr contains
JSON errors. Exit codes are 0 for success and 1 for failure. DELETE returns
`{"ok":true}`. Error diagnostics include the HTTP status and a known error code,
without arbitrary server response text or secrets.
