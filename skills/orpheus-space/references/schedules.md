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

Run `orpheus-space schedule settings --json` before adding ENV references.
Its JSON response contains `allowed_env_from`, an array of ENV names permitted
by this Space installation, and `base_env_from`, the names already included in
every task. These are API response fields, not environment variables to read
from the agent's shell. For example:

```json
{
  "allowed_env_from": ["REPORT_API_KEY", "REPORT_HOST"],
  "base_env_from": ["REPORT_HOST"],
  "browser_auth": "api_only"
}
```

Use only names returned in `allowed_env_from` for the task's `env_from`. Base
names are included automatically. The core worker supplies the corresponding
values at execution time; never request secret values or put them in JSON or
prompts.

- `session_mode=new` starts each run in a new session; this is the default.
- `session_mode=reuse` preserves conversation history. Changes to model, env_from,
  session mode, or base configuration start a new session after the previous run ends.
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
