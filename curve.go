package swap

import (
	"fmt"
	"math"
)

// Curve is a market curve injected by the caller. All queries are
// relative to an as-of date so fixings are deterministic for a given
// curve instance.
type Curve interface {
	// DiscountFactor returns the discount factor from asOf to d.
	// It must return 1 when d == asOf and be deterministic.
	DiscountFactor(asOf, d Date) float64
}

// ForwardRate derives the simply-compounded forward rate for the
// accrual period [start, end) from discount factors.
func ForwardRate(c Curve, asOf, start, end Date, dcf float64) float64 {
	if !start.Before(end) || dcf <= 0 {
		return 0
	}
	dfStart := c.DiscountFactor(asOf, start)
	dfEnd := c.DiscountFactor(asOf, end)
	if dfEnd <= 0 {
		return 0
	}
	return (dfStart/dfEnd - 1) / dcf
}

// FlatCurve is a Curve with a single continuously-compounded rate.
type FlatCurve struct {
	Rate float64
}

// DiscountFactor implements Curve using ACT/365F year fractions.
func (c FlatCurve) DiscountFactor(asOf, d Date) float64 {
	t := Act365F.Fraction(asOf, d)
	return math.Exp(-c.Rate * t)
}

// ZeroCurve is a Curve bootstrapped from zero rates at pillar dates,
// continuously compounded, linearly interpolated on zero rates.
type ZeroCurve struct {
	AsOf    Date
	Pillars []Date    // strictly increasing, all after AsOf
	Rates   []float64 // same length as Pillars
}

// NewZeroCurve validates and builds a ZeroCurve.
func NewZeroCurve(asOf Date, pillars []Date, rates []float64) (ZeroCurve, error) {
	if len(pillars) == 0 {
		return ZeroCurve{}, fmt.Errorf("zero curve needs at least one pillar")
	}
	if len(pillars) != len(rates) {
		return ZeroCurve{}, fmt.Errorf("pillars and rates length mismatch")
	}
	for i, p := range pillars {
		if !p.After(asOf) {
			return ZeroCurve{}, fmt.Errorf("pillar %s must be after as-of %s", p, asOf)
		}
		if i > 0 && !p.After(pillars[i-1]) {
			return ZeroCurve{}, fmt.Errorf("pillars must be strictly increasing")
		}
	}
	cp := make([]Date, len(pillars))
	copy(cp, pillars)
	cr := make([]float64, len(rates))
	copy(cr, rates)
	return ZeroCurve{AsOf: asOf, Pillars: cp, Rates: cr}, nil
}

// DiscountFactor implements Curve.
func (c ZeroCurve) DiscountFactor(asOf, d Date) float64 {
	if !asOf.Before(d) {
		if asOf.Equal(d) {
			return 1
		}
		// Past dates relative to as-of: invert the forward factor.
		return 1 / c.DiscountFactor(d, asOf)
	}
	return c.df0(d) / c.df0(asOf)
}

// df0 is the discount factor from the curve's own as-of date to d.
func (c ZeroCurve) df0(d Date) float64 {
	t := Act365F.Fraction(c.AsOf, d)
	if t <= 0 {
		return 1
	}
	return math.Exp(-c.zeroRate(d) * t)
}

// zeroRate interpolates the zero rate at d linearly between pillars,
// with flat extrapolation outside the pillar range.
func (c ZeroCurve) zeroRate(d Date) float64 {
	if !d.After(c.Pillars[0]) {
		return c.Rates[0]
	}
	last := len(c.Pillars) - 1
	if !d.Before(c.Pillars[last]) {
		return c.Rates[last]
	}
	for i := 1; i <= last; i++ {
		if !d.After(c.Pillars[i]) {
			t0 := Act365F.Fraction(c.AsOf, c.Pillars[i-1])
			t1 := Act365F.Fraction(c.AsOf, c.Pillars[i])
			t := Act365F.Fraction(c.AsOf, d)
			w := (t - t0) / (t1 - t0)
			return c.Rates[i-1] + w*(c.Rates[i]-c.Rates[i-1])
		}
	}
	return c.Rates[last]
}
