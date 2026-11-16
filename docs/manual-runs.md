# Manual schedule runs

`POST /api/v1/schedules/{id}/run` requests a new occurrence using the schedule's
saved settings. It works for both active and paused schedules. It does not change
the cron, activity, next scheduled time or reusable context.

The caller needs the same permission as for editing the schedule. Browser requests
use the normal Origin and CSRF checks. Send no body and a nonzero UUID in
`Idempotency-Key`. A `202` response contains the stored occurrence, not a guarantee
that execution has started. Read its progress through the existing occurrence
and result endpoints.

Retry the same command with the same key. The key is scoped to the schedule and
returns the same occurrence with its current state, including after completion or
deletion. Current ownership permissions still apply. A new key requests a new run.
An unfinished occurrence, including an uncertain dispatch outcome, returns
`409 schedule_busy` for another command. A new command for a deleted schedule
returns `409 schedule_deleted`.

The worker prepares the settings through the same path as a scheduled run.
Edits before preparation apply to that occurrence. After preparation, retries use
the frozen request and its separate core idempotency key. `new` and `reuse` keep
their usual session behavior. A manual run after a failure does not reset context.

Occurrences expose `trigger=scheduled|manual`. For a manual occurrence,
`scheduled_at` records when Space accepted the command. It does not repeat the
reporting period of a previous failure. A successful manual run contributes to
`last_successful_run`. Cron periods overlapping a manual run follow the usual
skip rules.

Pause cancels only pending scheduled occurrences. Delete cancels pending
occurrences of either kind. Prepared requests and accepted runs retain the usual
reconciliation behavior after pause or deletion.

Deploy the updated worker before exposing the manual API. Migration 11 retains
existing occurrences as scheduled and leaves their request bytes unchanged.
Its Down migration refuses to remove manual history. Test rollback against the
target schema instead of deleting that history to downgrade.
