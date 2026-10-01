package schedule

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func validInput() Input {
	in := Defaults()
	in.Name = "Report"
	in.Prompt = "Summarize incidents"
	in.Cron = "0 10 * * 1-5"
	in.Timezone = "Europe/Moscow"
	return in
}
func instant(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}
func TestCronTimezonesAndDST(t *testing.T) {
	for _, tc := range []struct{ cron, zone, after, want string }{
		{"0 10 * * 1-5", "Europe/Moscow", "2026-10-02T07:00:00Z", "2026-10-05T07:00:00Z"},
		{"30 2 * * *", "Europe/Berlin", "2026-03-28T02:00:00Z", "2026-03-30T00:30:00Z"},
		{"30 2 * * *", "Europe/Berlin", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z"},
		{"0 0 29 2 *", "UTC", "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"0 0 1 * MON", "UTC", "2026-10-02T00:00:00Z", "2026-10-05T00:00:00Z"},
	} {
		t.Run(tc.zone+tc.after, func(t *testing.T) {
			got, err := Next(tc.cron, tc.zone, instant(tc.after))
			if err != nil || !got.Equal(instant(tc.want)) || got.Location() != time.UTC {
				t.Fatalf("got %v %v want %s", got, err, tc.want)
			}
		})
	}
	for _, cron := range []string{"", "@daily", "@every 1h", "0 0 0 * * *", "TZ=UTC 0 0 * * *", "CRON_TZ=UTC 0 0 * * *", "0 0 30 2 *", "*/0 * * * *", "61 * * * *"} {
		if _, err := Next(cron, "UTC", instant("2026-01-01T00:00:00Z")); err == nil {
			t.Errorf("accepted %q", cron)
		}
	}
	for _, zone := range []string{"", "Local", "missing/zone", "UTC x"} {
		if _, err := Next("* * * * *", zone, time.Now()); err == nil {
			t.Errorf("accepted zone %q", zone)
		}
	}
}
func TestNormalizeAndPatch(t *testing.T) {
	in := validInput()
	in.OwnerEmail = new(" Alice@EXAMPLE.com ")
	in.EnvFrom = []string{"B", "A"}
	in.Model = new(" model ")
	got, err := Normalize(in, []string{"A", "B"}, time.Now())
	if err != nil || *got.OwnerEmail != "alice@example.com" || *got.Model != "model" || !slices.Equal(got.EnvFrom, []string{"A", "B"}) {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = Patch(got, []byte(`{"model":null,"owner_email":null,"env_from":[]}`))
	if err != nil || got.Model != nil || got.OwnerEmail != nil || len(got.EnvFrom) != 0 || got.Prompt != in.Prompt {
		t.Fatalf("%+v %v", got, err)
	}
	for _, raw := range []string{`{"name":null}`, `{"status":null}`, `{"env_from":null}`, `{"id":"fake"}`, `null`, `[]`, `{"prompt":5}`} {
		if _, err := Patch(in, []byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*Input)
	}{
		{"name", func(i *Input) { i.Name = " " }}, {"prompt", func(i *Input) { i.Prompt = "\n" }}, {"model", func(i *Input) { i.Model = new("") }},
		{"status", func(i *Input) { i.Status = "deleted" }}, {"session_mode", func(i *Input) { i.SessionMode = "other" }},
		{"email", func(i *Input) { i.OwnerEmail = new("Alice <alice@example.com>") }}, {"empty email", func(i *Input) { i.OwnerEmail = new("") }},
		{"unknown env", func(i *Input) { i.EnvFrom = []string{"SECRET"} }}, {"duplicate env", func(i *Input) { i.EnvFrom = []string{"A", "A"} }}, {"bad env", func(i *Input) { i.EnvFrom = []string{"A=B"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.change(&in)
			_, err := Normalize(in, []string{"A"}, time.Now())
			problem, ok := errors.AsType[*Error](err)
			if !ok || problem.Status != 422 {
				t.Fatalf("validation %v", err)
			}
		})
	}
}

func TestPatchDoesNotMutateInput(t *testing.T) {
	for _, raw := range []string{`{"model":"new","owner_email":"new@example.com","env_from":["C"]}`, `{"model":null,"owner_email":null,"env_from":[]}`, `{"model":"new","env_from":["C",42]}`} {
		current := validInput()
		current.Model = new("old")
		current.OwnerEmail = new("old@example.com")
		current.EnvFrom = []string{"A", "B"}
		_, _ = Patch(current, []byte(raw))
		if *current.Model != "old" || *current.OwnerEmail != "old@example.com" || !slices.Equal(current.EnvFrom, []string{"A", "B"}) {
			t.Fatalf("patch mutated original: %+v", current)
		}
	}
}
