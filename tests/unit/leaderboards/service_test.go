package leaderboards

import (
	"testing"
	"time"
)

func TestPeriodBoundsVietnam(t *testing.T) {
	cases := []struct{ period, now, start, end string }{
		{"week", "2026-10-04T17:00:00Z", "2026-10-04T17:00:00Z", "2026-10-11T17:00:00Z"},
		{"week", "2026-10-04T16:59:59Z", "2026-09-27T17:00:00Z", "2026-10-04T17:00:00Z"},
		{"month", "2026-09-30T17:00:00Z", "2026-09-30T17:00:00Z", "2026-10-31T17:00:00Z"},
		{"month", "2026-09-30T16:59:59Z", "2026-08-31T17:00:00Z", "2026-09-30T17:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.period+c.now, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, c.now)
			start, end, err := Bounds(c.period, now)
			if err != nil || start.UTC().Format(time.RFC3339) != c.start || end.UTC().Format(time.RFC3339) != c.end {
				t.Fatalf("bounds: %v %v %v", start, end, err)
			}
		})
	}
	start, end, err := Bounds("all", time.Now())
	if err != nil || start != nil || end != nil {
		t.Fatal("all must be unbounded")
	}
	if _, _, err = Bounds("daily", time.Now()); err == nil {
		t.Fatal("invalid period accepted")
	}
}
