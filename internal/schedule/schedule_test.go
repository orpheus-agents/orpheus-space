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
	in.Services = []string{"b", "a"}
	in.Model = new(" model ")
	got, err := Normalize(in, time.Now())
	if err != nil || *got.OwnerEmail != "alice@example.com" || *got.Model != "model" || !slices.Equal(got.Services, []string{"a", "b"}) {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = Patch(got, []byte(`{"model":null,"owner_email":null,"services":[]}`))
	if err != nil || got.Model != nil || got.OwnerEmail != nil || len(got.Services) != 0 || got.Prompt != in.Prompt {
		t.Fatalf("%+v %v", got, err)
	}
	for _, raw := range []string{`{"name":null}`, `{"status":null}`, `{"services":null}`, `{"id":"fake"}`, `null`, `[]`, `{"prompt":5}`} {
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
		{"uppercase service", func(i *Input) { i.Services = []string{"SECRET"} }}, {"duplicate service", func(i *Input) { i.Services = []string{"a", "a"} }}, {"bad service", func(i *Input) { i.Services = []string{"A=B"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.change(&in)
			_, err := Normalize(in, time.Now())
			problem, ok := errors.AsType[*Error](err)
			if !ok || problem.Status != 422 {
				t.Fatalf("validation %v", err)
			}
		})
	}
}

func TestPatchDoesNotMutateInput(t *testing.T) {
	for _, raw := range []string{`{"model":"new","owner_email":"new@example.com","services":["c"]}`, `{"model":null,"owner_email":null,"services":[]}`, `{"model":"new","services":["c",42]}`} {
		current := validInput()
		current.Model = new("old")
		current.OwnerEmail = new("old@example.com")
		current.Services = []string{"a", "b"}
		_, _ = Patch(current, []byte(raw))
		if *current.Model != "old" || *current.OwnerEmail != "old@example.com" || !slices.Equal(current.Services, []string{"a", "b"}) {
			t.Fatalf("patch mutated original: %+v", current)
		}
	}
}

func TestLatestDue(t *testing.T) {
	for _, tc := range []struct{ cron, zone, start, end string }{
		{"* * * * *", "UTC", "2010-01-01T00:00:00Z", "2026-10-01T12:34:56Z"},
		{"30 2 * * *", "Europe/Berlin", "2026-10-24T00:30:00Z", "2026-10-25T01:40:00Z"},
		{"30 2 * * *", "Europe/Berlin", "2026-03-28T01:30:00Z", "2026-03-30T01:00:00Z"},
		{"0 0 29 2 *", "UTC", "2020-02-29T00:00:00Z", "2026-10-01T00:00:00Z"},
	} {
		first, now := instant(tc.start), instant(tc.end)
		got, err := Latest(tc.cron, tc.zone, first, now)
		if err != nil {
			t.Fatal(err)
		}
		next, err := Next(tc.cron, tc.zone, got)
		if err != nil || !next.After(now) || got.After(now) || got.Before(first) {
			t.Fatal(got, next, err)
		}
		exact, err := Next(tc.cron, tc.zone, got.Add(-time.Second))
		if err != nil || !exact.Equal(got) {
			t.Fatal("not a cron occurrence", got, err)
		}
	}
}
