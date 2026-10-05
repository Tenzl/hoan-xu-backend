package rewards

import (
	"errors"
	"math"
	"time"
)

func LocalDay(t time.Time) string { return t.In(time.FixedZone("ICT", 7*3600)).Format("2006-01-02") }
func NextCheckin(last string, streak int, day string) (int, int, error) {
	d, e := time.Parse("2006-01-02", day)
	if e != nil {
		return 0, 0, e
	}
	if last == day {
		return 0, 0, errors.New("already checked in")
	}
	next := 1
	if last == d.AddDate(0, 0, -1).Format("2006-01-02") {
		next = streak + 1
	}
	bonus := map[int]int{3: 2, 7: 5, 14: 10, 30: 30}
	return next, 1 + bonus[next], nil
}
func ExchangeAmount(n int64) (int64, error) {
	if n < 10 || n%10 != 0 || n > math.MaxInt64/300 {
		return 0, errors.New("coins must be a positive multiple of ten")
	}
	return n * 300, nil
}
