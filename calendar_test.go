package swap

import "testing"

func TestWeekendCalendar(t *testing.T) {
	cal := NewWeekendCalendar(MustParseDate("2024-06-03"))
	if cal.IsBusinessDay(MustParseDate("2024-06-01")) { // Saturday
		t.Error("Saturday must not be a business day")
	}
	if cal.IsBusinessDay(MustParseDate("2024-06-02")) { // Sunday
		t.Error("Sunday must not be a business day")
	}
	if cal.IsBusinessDay(MustParseDate("2024-06-03")) { // holiday Monday
		t.Error("holiday must not be a business day")
	}
	if !cal.IsBusinessDay(MustParseDate("2024-06-04")) { // Tuesday
		t.Error("plain Tuesday must be a business day")
	}
}

func TestAdjustFollowing(t *testing.T) {
	cal := NewWeekendCalendar()
	got := Adjust(cal, Following, MustParseDate("2024-06-01")) // Sat
	if want := MustParseDate("2024-06-03"); !got.Equal(want) {
		t.Errorf("following got %s, want %s", got, want)
	}
	// Business days stay put.
	got = Adjust(cal, Following, MustParseDate("2024-06-04"))
	if want := MustParseDate("2024-06-04"); !got.Equal(want) {
		t.Errorf("following got %s, want %s", got, want)
	}
}

func TestAdjustPreceding(t *testing.T) {
	cal := NewWeekendCalendar()
	got := Adjust(cal, Preceding, MustParseDate("2024-06-01")) // Sat
	if want := MustParseDate("2024-05-31"); !got.Equal(want) { // Fri
		t.Errorf("preceding got %s, want %s", got, want)
	}
}

func TestAdjustModifiedFollowing(t *testing.T) {
	cal := NewWeekendCalendar()
	// 2024-08-31 is a Saturday; following crosses into September,
	// so modified following rolls back to Friday 2024-08-30.
	got := Adjust(cal, ModifiedFollowing, MustParseDate("2024-08-31"))
	if want := MustParseDate("2024-08-30"); !got.Equal(want) {
		t.Errorf("modified following got %s, want %s", got, want)
	}
	// Within the month it behaves like following.
	got = Adjust(cal, ModifiedFollowing, MustParseDate("2024-06-01"))
	if want := MustParseDate("2024-06-03"); !got.Equal(want) {
		t.Errorf("modified following got %s, want %s", got, want)
	}
}

func TestAdjustUnadjusted(t *testing.T) {
	cal := NewWeekendCalendar()
	d := MustParseDate("2024-06-01")
	if got := Adjust(cal, Unadjusted, d); !got.Equal(d) {
		t.Errorf("unadjusted got %s, want %s", got, d)
	}
}

func TestAddBusinessDays(t *testing.T) {
	cal := NewWeekendCalendar()
	fri := MustParseDate("2024-05-31")
	mon := MustParseDate("2024-06-03")
	if got := AddBusinessDays(cal, fri, 1); !got.Equal(mon) {
		t.Errorf("+1bd got %s, want %s", got, mon)
	}
	if got := AddBusinessDays(cal, mon, -1); !got.Equal(fri) {
		t.Errorf("-1bd got %s, want %s", got, fri)
	}
	if got, want := AddBusinessDays(cal, fri, 3), MustParseDate("2024-06-05"); !got.Equal(want) {
		t.Errorf("+3bd got %s, want %s", got, want)
	}
	if got := AddBusinessDays(cal, fri, 0); !got.Equal(fri) {
		t.Errorf("+0bd got %s, want %s", got, fri)
	}
}
