# Schedules

Create tasks only when requested by the user. Clarify any missing recurrence,
timezone, and result destination. Cron has five fields; timezone is an IANA name,
such as Europe/Moscow. Check upcoming times with `schedule preview`.

Write a self-contained prompt: a scheduled run does not receive the originating
conversation. Replace references such as "here" with an explicit destination,
using channel, conversation, or thread identifiers supplied by the connector.
For example, Mattermost provides `channel.id` and `channel.name` in message front
matter; include the thread ID when needed. Do not invent a delivery mechanism:
sending results to a messenger or another application requires the corresponding
tools and credentials in the task's environment. Space stores status and the run
reference; `result` retrieves the outcome from core without publishing it elsewhere.

At execution, Space adds host-generated front matter before the saved prompt. It
contains `source: orpheus_space`, the schedule and occurrence IDs, local run times,
the timezone, and `owner_email` for an owned schedule. The main skill defines the
narrow ownership rules for using this identity to manage the originating schedule.

Before creating a task, read `orpheus-space profiles --json` and
`orpheus-space schedule settings --json`. Resolve `default_template` or the user's
explicit name with `orpheus-space templates --name '<name>' --json`, without an
active filter. Honor the user's explicit choice. Choose only an existing entry
with `active: true`; a missing or inactive default requires an explicit choice.

To browse choices, use `orpheus-space templates --active=true --name-contains
'<text>' --limit 50 --json`. This is one page, not the whole catalog. Pass a
returned `next_cursor` or `prev_cursor` through `--cursor` with the same filters;
restart pagination after changing filters. Descriptions explain each entry.
`is_default` indicates the configured name and does not guarantee activity.
Never invent a name or silently replace a removed/inactive choice.

Before any edit or resume, confirm the effective template by exact name and
require `active: true`, even if the patch does not change the template. The server
checks again. Do not change existing tasks' selections unless requested.
Catalog failures block edits and resume; they do not block pause or delete.
Use `schedule pause` or a patch with only `{"status":"paused"}` for the exception.
Creation still requires an active template when initially paused. Omitting
profile/template on creation saves the configured defaults after validation.

Read `orpheus-space services --json` before selecting services. The response has
`items` with `code`, `name`, `description` and `env_from` (ENV names, never values).
Choose the codes needed for the task's sources and delivery tools and pass them in
`services`. There are no base or default services: `services: []` gives no service
access. All catalog entries are selectable, including Orpheus Space.

Never invent a code or ask for secret values. Orpheus resolves the selected
services from its configuration and the worker supplies ENV values at execution.
Unknown codes return 422; catalog unavailability returns 503 when validation is
needed. An unchanged service selection is not revalidated on ordinary edits; the template still is.
Resuming a task validates its complete service selection. Removed services can
be cleared or replaced with available choices.

- `session_mode=new` starts each run in a new session; this is the default.
- `session_mode=reuse` preserves conversation history. Changes to profile, template, model,
  services, session mode, or effective base configuration start a new session after the previous run ends.
Changes to a service definition do not expand an existing reusable session. Use
`reset-session` when the next execution must pick up the updated definition.

- `model` is optional; null leaves the choice to the core profile.
- `status=active` / `paused` means running / paused. Pausing does not cancel a run
  already accepted by core.
- After downtime, only the latest eligible period executes. Periods overlapping
  an active run are skipped. Unknown outcomes block new executions until reconciled;
  do not recreate a task to bypass this protection.
- `reset-session` clears the reusable session reference and returns 409 while an
  occurrence is active.
- DELETE hides a task while preserving history. Delete only when requested.

After a successful write, report the name, ID, cron, timezone, status, and next
scheduled time. Distinguish acceptance by core (`state=accepted`) from the agent's
execution status (`run_status`). Check `observed_at` and `sync_error_code` when
reads fail: the stored status may be stale.
