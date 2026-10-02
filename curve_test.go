package swap

import (
	"math"
	"testing"
)

func TestFlatCurveDiscountFactor(t *testing.T) {
	c := FlatCurve{Rate: 0.05}
	asOf := MustParseDate("2024-01-01")
	if got := c.DiscountFactor(asOf, asOf); got != 1 {
		t.Errorf("DF(asOf, asOf) = %v, want 1", got)
	}
	d := MustParseDate("2025-01-01")
	want := math.Exp(-0.05 * Act365F.Fraction(asOf, d))
	almostEq(t, c.DiscountFactor(asOf, d), want)
	// Discount factors decrease with tenor for positive rates.
	near := c.DiscountFactor(asOf, MustParseDate("2024-07-01"))
	far := c.DiscountFactor(asOf, d)
	if !(near > far) {
		t.Errorf("DF should decrease with tenor: %v vs %v", near, far)
	}
}

func TestZeroCurveValidation(t *testing.T) {
	asOf := MustParseDate("2024-01-01")
	if _, err := NewZeroCurve(asOf, nil, nil); err == nil {
		t.Error("empty pillars must be rejected")
	}
	if _, err := NewZeroCurve(asOf,
		[]Date{MustParseDate("2025-01-01")}, nil); err == nil {
		t.Error("length mismatch must be rejected")
	}
	if _, err := NewZeroCurve(asOf,
		[]Date{MustParseDate("2023-06-01")}, []float64{0.05}); err == nil {
		t.Error("pillar before as-of must be rejected")
	}
	if _, err := NewZeroCurve(asOf,
		[]Date{MustParseDate("2025-01-01"), MustParseDate("2024-06-01")},
		[]float64{0.05, 0.06}); err == nil {
		t.Error("unsorted pillars must be rejected")
	}
}

func TestZeroCurveDiscountFactor(t *testing.T) {
	asOf := MustParseDate("2024-01-01")
	p1 := MustParseDate("2025-01-01")
	p2 := MustParseDate("2026-01-01")
	c, err := NewZeroCurve(asOf, []Date{p1, p2}, []float64{0.05, 0.06})
	if err != nil {
		t.Fatal(err)
	}
	// At pillars the zero rate applies exactly.
	t1 := Act365F.Fraction(asOf, p1)
	almostEq(t, c.DiscountFactor(asOf, p1), math.Exp(-0.05*t1))
	t2 := Act365F.Fraction(asOf, p2)
	almostEq(t, c.DiscountFactor(asOf, p2), math.Exp(-0.06*t2))
	// Forward discount factor between the pillars.
	almostEq(t, c.DiscountFactor(p1, p2), math.Exp(-(0.06*t2 - 0.05*t1)))
	// Interpolated mid-pillar date sits between the pillar DFs.
	mid := MustParseDate("2025-07-01")
	dfMid := c.DiscountFactor(asOf, mid)
	if !(dfMid < c.DiscountFactor(asOf, p1) && dfMid > c.DiscountFactor(asOf, p2)) {
		t.Errorf("interpolated DF %v out of bounds", dfMid)
	}
}

func TestForwardRateDeterministic(t *testing.T) {
	c := FlatCurve{Rate: 0.05}
	asOf := MustParseDate("2024-01-01")
	start := MustParseDate("2024-06-01")
	end := MustParseDate("2024-12-01")
	dcf := Act365F.Fraction(start, end)
	want := (math.Exp(0.05*dcf) - 1) / dcf
	got1 := ForwardRate(c, asOf, start, end, dcf)
	got2 := ForwardRate(c, asOf, start, end, dcf)
	almostEq(t, got1, want)
	if got1 != got2 {
		t.Errorf("forward rate not reproducible: %v vs %v", got1, got2)
	}
}
