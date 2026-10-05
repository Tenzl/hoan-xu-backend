package rewards

import (
	"testing"
	"time"
)

func TestCheckinRules(t *testing.T) {
	cases := []struct {
		last   string
		streak int
		day    string
		want   int
		award  int
	}{
		{"", 0, "2026-10-05", 1, 1},
		{"2026-10-04", 2, "2026-10-05", 3, 3},
		{"2026-10-04", 13, "2026-10-05", 14, 11},
		{"2026-10-04", 29, "2026-10-05", 30, 31},
		{"2026-10-04", 6, "2026-10-05", 7, 6},
		{"2026-10-03", 6, "2026-10-05", 1, 1},
		{"2026-10-04", 30, "2026-10-05", 31, 1},
	}
	for _, c := range cases {
		st, award, err := NextCheckin(c.last, c.streak, c.day)
		if err != nil || st != c.want || award != c.award*300 {
			t.Fatalf("%+v: %d %d %v", c, st, award, err)
		}
	}
	if _, _, err := NextCheckin("2026-10-05", 7, "2026-10-05"); err == nil {
		t.Fatal("duplicate day must fail")
	}
	if LocalDay(time.Date(2026, 10, 4, 17, 0, 0, 0, time.UTC)) != "2026-10-05" {
		t.Fatal("must use Vietnam day")
	}
}

func TestExchangeValidation(t *testing.T) {
	for _, n := range []int64{-10, 0, 9, 11} {
		if _, err := ExchangeAmount(n); err == nil {
			t.Fatalf("accepted %d", n)
		}
	}
	if v, e := ExchangeAmount(20); v != 6000 || e != nil {
		t.Fatal(v, e)
	}
}
