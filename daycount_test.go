package swap

import (
	"math"
	"testing"
)

func almostEq(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAct360(t *testing.T) {
	start := MustParseDate("2024-01-01")
	end := MustParseDate("2024-07-01")
	almostEq(t, Act360.Fraction(start, end), 182.0/360.0)
}

func TestAct365F(t *testing.T) {
	start := MustParseDate("2024-01-01")
	end := MustParseDate("2024-07-01")
	almostEq(t, Act365F.Fraction(start, end), 182.0/365.0)
}

func TestThirty360(t *testing.T) {
	// Plain six months.
	almostEq(t, Thirty360.Fraction(
		MustParseDate("2024-01-15"), MustParseDate("2024-07-15")), 0.5)
	// 31st day rolls back to 30.
	almostEq(t, Thirty360.Fraction(
		MustParseDate("2024-01-31"), MustParseDate("2024-07-31")), 0.5)
	// February is a 30-day month under 30/360.
	almostEq(t, Thirty360.Fraction(
		MustParseDate("2024-02-28"), MustParseDate("2024-03-28")), 30.0/360.0)
}

func TestActAct(t *testing.T) {
	// A full calendar year is exactly 1, leap or not.
	almostEq(t, ActAct.Fraction(
		MustParseDate("2023-01-01"), MustParseDate("2024-01-01")), 1.0)
	almostEq(t, ActAct.Fraction(
		MustParseDate("2024-01-01"), MustParseDate("2025-01-01")), 1.0)
	// Crossing a leap-year boundary splits the fraction.
	got := ActAct.Fraction(
		MustParseDate("2023-07-01"), MustParseDate("2024-07-01"))
	almostEq(t, got, 184.0/365.0+182.0/366.0)
}

func TestDayCountRoundTrip(t *testing.T) {
	for _, dc := range []DayCount{Act360, Act365F, Thirty360, ActAct} {
		parsed, err := ParseDayCount(dc.String())
		if err != nil {
			t.Fatalf("ParseDayCount(%q): %v", dc.String(), err)
		}
		if parsed != dc {
			t.Errorf("round trip %v != %v", parsed, dc)
		}
	}
	if _, err := ParseDayCount("BOGUS"); err == nil {
		t.Error("expected error for unknown convention")
	}
}
