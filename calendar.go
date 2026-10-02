package irswap

import "time"

// MapCalendarRegistry is an in-memory CalendarRegistry.
type MapCalendarRegistry struct {
	calendars map[string]BusinessCalendar
}

func NewMapCalendarRegistry(calendars ...BusinessCalendar) *MapCalendarRegistry {
	registry := &MapCalendarRegistry{calendars: make(map[string]BusinessCalendar)}
	for _, calendar := range calendars {
		if calendar != nil {
			registry.calendars[calendar.ID()] = calendar
		}
	}
	return registry
}

func (r *MapCalendarRegistry) Get(id string) (BusinessCalendar, error) {
	calendar, ok := r.calendars[id]
	if !ok {
		return nil, ErrInvalidContract
	}
	return calendar, nil
}

// WeekendCalendar treats Saturday and Sunday as non-business days.
type WeekendCalendar struct {
	id string
}

func NewWeekendCalendar(id string) WeekendCalendar {
	return WeekendCalendar{id: id}
}

func (c WeekendCalendar) ID() string { return c.id }

func (c WeekendCalendar) IsBusinessDay(date time.Time) bool {
	day := date.Weekday()
	return day != time.Saturday && day != time.Sunday
}

// AdjustDate applies the requested business-day convention.
func AdjustDate(date time.Time, calendar BusinessCalendar, convention BusinessDayConvention) time.Time {
	if calendar == nil {
		return date
	}

	switch convention {
	case BusinessDayUnadjusted:
		return date
	case BusinessDayPreceding:
		return adjustBackward(date, calendar)
	case BusinessDayModifiedFollowing:
		adjusted := adjustForward(date, calendar)
		if adjusted.Month() != date.Month() {
			adjusted = adjustBackward(date, calendar)
		}
		return adjusted
	case BusinessDayFollowing, "":
		return adjustForward(date, calendar)
	default:
		return date
	}
}

func adjustForward(date time.Time, calendar BusinessCalendar) time.Time {
	for !calendar.IsBusinessDay(date) {
		date = date.AddDate(0, 0, 1)
	}
	return date
}

func adjustBackward(date time.Time, calendar BusinessCalendar) time.Time {
	for !calendar.IsBusinessDay(date) {
		date = date.AddDate(0, 0, -1)
	}
	return date
}

// AddPeriods returns start plus n payment/reset periods for the frequency.
func AddPeriods(start time.Time, frequency Frequency, count int) time.Time {
	switch frequency {
	case FrequencyMonthly:
		return start.AddDate(0, count, 0)
	case FrequencyQuarterly:
		return start.AddDate(0, 3*count, 0)
	case FrequencySemiannual:
		return start.AddDate(0, 6*count, 0)
	case FrequencyAnnual:
		return start.AddDate(count, 0, 0)
	case FrequencyZero:
		return start
	default:
		return start
	}
}
