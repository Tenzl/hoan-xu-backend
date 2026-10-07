package cashback

import (
	"fmt"
	"time"
	_ "time/tzdata"
)

type PeriodConfig struct {
	PeriodMonths int    `json:"periodMonths"`
	AnchorDate   string `json:"anchorDate"`
	DateBasis    string `json:"dateBasis"`
}
type MembershipProgress struct {
	StartingNameVI          string    `json:"startingNameVi"`
	StartingNameEN          string    `json:"startingNameEn"`
	NextPeriodNameVI        string    `json:"nextPeriodNameVi"`
	NextPeriodNameEN        string    `json:"nextPeriodNameEn"`
	ApprovedOrders          int64     `json:"approvedOrders"`
	StartingTierCode        string    `json:"startingTierCode"`
	NextPeriodTierCode      string    `json:"nextPeriodTierCode"`
	PeriodGoldTotal         int64     `json:"periodGoldTotal"`
	PreviousPeriodGoldTotal int64     `json:"previousPeriodGoldTotal"`
	GoldToNext              int64     `json:"goldToNext"`
	GoldToMaintain          int64     `json:"goldToMaintain"`
	PeriodStartsAt          time.Time `json:"periodStartsAt"`
	PeriodEndsAt            time.Time `json:"periodEndsAt"`
	AsOf                    time.Time `json:"asOf"`
	PeriodConfig
}

// Every boundary is anchored independently, so a February clamp never drifts March.
func PeriodBounds(p PeriodConfig, at time.Time) (previous, start, end time.Time, err error) {
	loc, e := time.LoadLocation("Asia/Ho_Chi_Minh")
	if e != nil {
		return previous, start, end, e
	}
	anchor, e := time.ParseInLocation("2006-01-02", p.AnchorDate, loc)
	if e != nil || anchor.Year() < 1 || anchor.Year() > 9999 || p.PeriodMonths < 1 || p.PeriodMonths > 12 || (p.DateBasis != "approved" && p.DateBasis != "ordered") {
		return previous, start, end, fmt.Errorf("invalid membership calendar")
	}
	local := at.In(loc)
	months := (local.Year()-anchor.Year())*12 + int(local.Month()-anchor.Month())
	n := months / p.PeriodMonths
	if months < 0 && months%p.PeriodMonths != 0 {
		n--
	}
	boundary := func(n int) time.Time {
		first := time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, n*p.PeriodMonths, 0)
		day := anchor.Day()
		last := first.AddDate(0, 1, -1).Day()
		if day > last {
			day = last
		}
		return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, loc)
	}
	start = boundary(n)
	if local.Before(start) {
		n--
		start = boundary(n)
	}
	end = boundary(n + 1)
	previous = boundary(n - 1)
	return
}
func SelectPeriod(p *Policy, previous, current int64) Membership {
	old := Select(p, previous)
	earned := Select(p, current)
	m := earned
	m.StartingNameVI = old.NameVI
	m.StartingNameEN = old.NameEN
	m.NextPeriodNameVI = earned.NameVI
	m.NextPeriodNameEN = earned.NameEN
	m.StartingTierCode = old.Code
	m.NextPeriodTierCode = earned.Code
	m.PreviousPeriodGoldTotal = previous
	m.PeriodConfig = p.PeriodConfig
	if old.MinGold > earned.MinGold {
		m.Tier = old.Tier
		m.NextTier = old.NextTier
		m.GoldToNext = 0
		if m.NextTier != nil {
			m.GoldToNext = m.NextTier.MinGold - current
		}
	}
	m.GoldToMaintain = max(int64(0), m.MinGold-current)
	return m
}
