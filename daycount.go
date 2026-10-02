package swap

import "fmt"

// DayCount is a day-count convention used to compute accrual factors.
type DayCount int

const (
	// Act360 uses actual days over a 360-day year.
	Act360 DayCount = iota
	// Act365F uses actual days over a fixed 365-day year.
	Act365F
	// Thirty360 uses the US 30/360 convention.
	Thirty360
	// ActAct uses ACT/ACT (ISDA): split accrual at year boundaries.
	ActAct
)

func (dc DayCount) String() string {
	switch dc {
	case Act360:
		return "ACT/360"
	case Act365F:
		return "ACT/365F"
	case Thirty360:
		return "30/360"
	case ActAct:
		return "ACT/ACT"
	default:
		return fmt.Sprintf("DayCount(%d)", int(dc))
	}
}

// ParseDayCount parses a day-count convention name.
func ParseDayCount(s string) (DayCount, error) {
	switch s {
	case "ACT/360":
		return Act360, nil
	case "ACT/365F":
		return Act365F, nil
	case "30/360":
		return Thirty360, nil
	case "ACT/ACT":
		return ActAct, nil
	default:
		return 0, fmt.Errorf("unknown day count convention %q", s)
	}
}

// valid reports whether dc is a known convention.
func (dc DayCount) valid() bool {
	switch dc {
	case Act360, Act365F, Thirty360, ActAct:
		return true
	default:
		return false
	}
}

// Fraction returns the year fraction for accrual over [start, end).
func (dc DayCount) Fraction(start, end Date) float64 {
	switch dc {
	case Act360:
		return float64(start.DaysTo(end)) / 360
	case Act365F:
		return float64(start.DaysTo(end)) / 365
	case Thirty360:
		d1 := start.Day()
		d2 := end.Day()
		if d1 == 31 {
			d1 = 30
		}
		if d2 == 31 && d1 == 30 {
			d2 = 30
		}
		y1, m1 := start.Year(), int(start.Month())
		y2, m2 := end.Year(), int(end.Month())
		days := 360*(y2-y1) + 30*(m2-m1) + (d2 - d1)
		return float64(days) / 360
	case ActAct:
		// ACT/ACT (ISDA): split the accrual at each Jan 1 and divide
		// each portion by the number of days in its calendar year.
		if !start.Before(end) {
			return 0
		}
		var frac float64
		cur := start
		for cur.Before(end) {
			nextYear := NewDate(cur.Year()+1, 1, 1)
			boundary := end
			if nextYear.Before(boundary) {
				boundary = nextYear
			}
			frac += float64(cur.DaysTo(boundary)) / float64(daysInYear(cur.Year()))
			cur = boundary
		}
		return frac
	default:
		panic("invalid day count convention")
	}
}

func daysInYear(year int) int {
	if isLeap(year) {
		return 366
	}
	return 365
}

func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}
