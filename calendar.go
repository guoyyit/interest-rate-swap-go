package swap

import "time"

// Calendar tells which dates are good business days.
// Implementations are injected by the caller.
type Calendar interface {
	IsBusinessDay(d Date) bool
}

// WeekendCalendar treats Saturdays, Sundays and an explicit holiday
// set as non-business days.
type WeekendCalendar struct {
	holidays map[Date]bool
}

// NewWeekendCalendar builds a weekend calendar with optional holidays.
func NewWeekendCalendar(holidays ...Date) *WeekendCalendar {
	c := &WeekendCalendar{holidays: make(map[Date]bool, len(holidays))}
	for _, h := range holidays {
		c.holidays[h] = true
	}
	return c
}

// IsBusinessDay reports whether d is a business day.
func (c *WeekendCalendar) IsBusinessDay(d Date) bool {
	wd := d.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	return !c.holidays[d]
}

// BusinessDayConvention adjusts non-business days for dates.
type BusinessDayConvention int

const (
	// Unadjusted leaves dates untouched.
	Unadjusted BusinessDayConvention = iota
	// Following rolls to the next business day.
	Following
	// ModifiedFollowing rolls forward unless it crosses a month,
	// in which case it rolls backward.
	ModifiedFollowing
	// Preceding rolls to the previous business day.
	Preceding
)

// valid reports whether bdc is a known convention.
func (bdc BusinessDayConvention) valid() bool {
	switch bdc {
	case Unadjusted, Following, ModifiedFollowing, Preceding:
		return true
	default:
		return false
	}
}

// Adjust applies a business-day convention to d using cal.
func Adjust(cal Calendar, bdc BusinessDayConvention, d Date) Date {
	switch bdc {
	case Unadjusted:
		return d
	case Following:
		for !cal.IsBusinessDay(d) {
			d = d.AddDays(1)
		}
		return d
	case ModifiedFollowing:
		adj := Adjust(cal, Following, d)
		if adj.Month() != d.Month() {
			return Adjust(cal, Preceding, d)
		}
		return adj
	case Preceding:
		for !cal.IsBusinessDay(d) {
			d = d.AddDays(-1)
		}
		return d
	default:
		panic("invalid business day convention")
	}
}

// AddBusinessDays shifts d by n business days (n may be negative).
// d itself must be a business day for the count to be meaningful;
// each step moves to the next (or previous) business day.
func AddBusinessDays(cal Calendar, d Date, n int) Date {
	step := 1
	if n < 0 {
		step = -1
		n = -n
	}
	for n > 0 {
		d = d.AddDays(step)
		if cal.IsBusinessDay(d) {
			n--
		}
	}
	return d
}
