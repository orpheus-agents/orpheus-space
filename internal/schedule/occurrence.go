package schedule

import (
	"time"

	"github.com/google/uuid"
)

type Occurrence struct {
	ID                 uuid.UUID  `json:"id"`
	ScheduleID         uuid.UUID  `json:"schedule_id"`
	ScheduledAt        time.Time  `json:"scheduled_at"`
	State              string     `json:"state"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	SessionID          *uuid.UUID `json:"session_id"`
	RunID              *uuid.UUID `json:"run_id"`
	RunStatus          *string    `json:"run_status"`
	ObservedAt         *time.Time `json:"observed_at"`
	ExecutionStartedAt *time.Time `json:"execution_started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	RunErrorCode       *string    `json:"run_error_code"`
	SyncErrorCode      *string    `json:"sync_error_code"`
	ErrorCode          *string    `json:"error_code"`
	Attempts           int        `json:"attempts"`
	NextAttemptAt      *time.Time `json:"next_attempt_at"`
}
type OccurrencePage struct {
	Items      []Occurrence `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// Latest finds the last due occurrence without iterating through every missed
// minute. Next is monotone in its starting instant, so binary search also works
// across DST changes. Work is logarithmic in the elapsed number of UTC minutes.
func Latest(expression, zone string, first, now time.Time) (time.Time, error) {
	latest := first
	lo, hi := first.Unix()/60, now.Unix()/60
	for lo <= hi {
		mid := lo + (hi-lo)/2
		candidate, err := Next(expression, zone, time.Unix(mid*60-1, 0))
		if err != nil {
			return time.Time{}, err
		}
		if candidate.After(now) {
			hi = mid - 1
		} else {
			latest = candidate
			lo = mid + 1
		}
	}
	return latest, nil
}
