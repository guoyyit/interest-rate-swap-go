package swap

import "testing"

func legForSchedule(freq Frequency) LegSpec {
	return LegSpec{
		Direction: Pay,
		RateType:  FixedRateLeg,
		FixedRate: 0.03,
		DayCount:  Act365F,
		PayFreq:   freq,
		BDC:       ModifiedFollowing,
		Notional:  []NotionalStep{{From: MustParseDate("2024-01-02"), Amount: MoneyFromUnits(1_000_000)}},
	}
}

func TestScheduleFrequencies(t *testing.T) {
	cal := NewWeekendCalendar()
	eff := MustParseDate("2024-01-02")
	mat := MustParseDate("2025-01-02")
	cases := []struct {
		freq    Frequency
		periods int
	}{
		{Monthly, 12},
		{Quarterly, 4},
		{Semiannual, 2},
		{Annual, 1},
	}
	for _, tc := range cases {
		ls, err := BuildLegSchedule(eff, mat, legForSchedule(tc.freq), cal)
		if err != nil {
			t.Fatalf("freq %v: %v", tc.freq, err)
		}
		if len(ls.Periods) != tc.periods {
			t.Errorf("freq %v: got %d periods, want %d", tc.freq, len(ls.Periods), tc.periods)
		}
		// Periods tile [eff, mat) without gaps.
		if !ls.Periods[0].AccrualStart.Equal(eff) {
			t.Errorf("freq %v: first period starts %s", tc.freq, ls.Periods[0].AccrualStart)
		}
		for i := 1; i < len(ls.Periods); i++ {
			if !ls.Periods[i-1].AccrualEnd.Equal(ls.Periods[i].AccrualStart) {
				t.Errorf("freq %v: gap before period %d", tc.freq, i)
			}
		}
		if !ls.Periods[len(ls.Periods)-1].AccrualEnd.Equal(mat) {
			t.Errorf("freq %v: last period ends %s", tc.freq, ls.Periods[len(ls.Periods)-1].AccrualEnd)
		}
	}
}

func TestScheduleFinalStub(t *testing.T) {
	cal := NewWeekendCalendar()
	ls, err := BuildLegSchedule(
		MustParseDate("2024-01-15"), MustParseDate("2024-08-15"),
		legForSchedule(Quarterly), cal)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls.Periods) != 3 {
		t.Fatalf("got %d periods, want 3", len(ls.Periods))
	}
	last := ls.Periods[2]
	if !last.AccrualStart.Equal(MustParseDate("2024-07-15")) ||
		!last.AccrualEnd.Equal(MustParseDate("2024-08-15")) {
		t.Errorf("stub period = %s..%s", last.AccrualStart, last.AccrualEnd)
	}
}

func TestSchedulePaymentDateAdjustment(t *testing.T) {
	cal := NewWeekendCalendar()
	spec := legForSchedule(Quarterly)
	spec.Notional[0].From = MustParseDate("2024-03-01")
	ls, err := BuildLegSchedule(
		MustParseDate("2024-03-01"), MustParseDate("2024-12-01"), spec, cal)
	if err != nil {
		t.Fatal(err)
	}
	// 2024-06-01 is a Saturday, 2024-09-01 and 2024-12-01 are Sundays.
	wants := []string{"2024-06-03", "2024-09-02", "2024-12-02"}
	if len(ls.Periods) != len(wants) {
		t.Fatalf("got %d periods, want %d", len(ls.Periods), len(wants))
	}
	for i, want := range wants {
		if got := ls.Periods[i].PaymentDate.String(); got != want {
			t.Errorf("period %d payment %s, want %s", i, got, want)
		}
		if !cal.IsBusinessDay(ls.Periods[i].PaymentDate) {
			t.Errorf("period %d payment %s is not a business day", i, ls.Periods[i].PaymentDate)
		}
	}
}

func TestScheduleResetDateWithLag(t *testing.T) {
	cal := NewWeekendCalendar()
	spec := legForSchedule(Quarterly)
	spec.RateType = FloatRateLeg
	spec.FixingLagDays = 2
	spec.Notional[0].From = MustParseDate("2024-03-01")
	ls, err := BuildLegSchedule(
		MustParseDate("2024-03-01"), MustParseDate("2024-06-01"), spec, cal)
	if err != nil {
		t.Fatal(err)
	}
	// Period start 2024-03-01 is a Friday; two business days back is
	// Wednesday 2024-02-28.
	if got, want := ls.Periods[0].ResetDate, MustParseDate("2024-02-28"); !got.Equal(want) {
		t.Errorf("reset date %s, want %s", got, want)
	}
}

func TestScheduleAmortizingNotional(t *testing.T) {
	cal := NewWeekendCalendar()
	spec := legForSchedule(Quarterly)
	spec.Notional = []NotionalStep{
		{From: MustParseDate("2024-01-02"), Amount: MoneyFromUnits(1_000_000)},
		{From: MustParseDate("2024-07-02"), Amount: MoneyFromUnits(500_000)},
	}
	ls, err := BuildLegSchedule(
		MustParseDate("2024-01-02"), MustParseDate("2025-01-02"), spec, cal)
	if err != nil {
		t.Fatal(err)
	}
	wants := []Money{MoneyFromUnits(1_000_000), MoneyFromUnits(1_000_000),
		MoneyFromUnits(500_000), MoneyFromUnits(500_000)}
	for i, want := range wants {
		if got := ls.Periods[i].Notional; got != want {
			t.Errorf("period %d notional %v, want %v", i, got, want)
		}
	}
}
