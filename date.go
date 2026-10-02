package swap

import "time"

// Date is a civil calendar date with no time-of-day component.
// The zero value is not a valid date; construct via NewDate or ParseDate.
type Date struct {
	t time.Time // always midnight UTC
}

// NewDate builds a Date from year, month and day.
func NewDate(year int, month time.Month, day int) Date {
	return Date{t: time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// ParseDate parses a "YYYY-MM-DD" formatted string.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return Date{}, err
	}
	return Date{t: t.In(time.UTC)}, nil
}

// MustParseDate is ParseDate that panics on error; for tests and literals.
func MustParseDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func (d Date) Year() int         { return d.t.Year() }
func (d Date) Month() time.Month { return d.t.Month() }
func (d Date) Day() int          { return d.t.Day() }
func (d Date) Weekday() time.Weekday {
	return d.t.Weekday()
}

// IsZero reports whether d is the zero Date.
func (d Date) IsZero() bool { return d.t.IsZero() }

// AddDate returns d shifted by the given years, months and days.
func (d Date) AddDate(years, months, days int) Date {
	return Date{t: d.t.AddDate(years, months, days)}
}

// AddDays returns d shifted by n days.
func (d Date) AddDays(n int) Date { return d.AddDate(0, 0, n) }

func (d Date) Before(o Date) bool { return d.t.Before(o.t) }
func (d Date) After(o Date) bool  { return d.t.After(o.t) }
func (d Date) Equal(o Date) bool  { return d.t.Equal(o.t) }

// Compare returns -1, 0 or 1 as d is before, equal to, or after o.
func (d Date) Compare(o Date) int {
	switch {
	case d.Before(o):
		return -1
	case d.After(o):
		return 1
	default:
		return 0
	}
}

// DaysTo returns the number of days from d to o (negative if o < d).
func (d Date) DaysTo(o Date) int {
	return int(o.t.Sub(d.t).Hours() / 24)
}

func (d Date) String() string { return d.t.Format("2006-01-02") }

// MarshalJSON encodes the date as "YYYY-MM-DD".
func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}

// UnmarshalJSON decodes a "YYYY-MM-DD" string.
func (d *Date) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	parsed, err := ParseDate(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}
