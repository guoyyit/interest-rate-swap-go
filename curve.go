package irswap

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/shopspring/decimal"
)

// FlatCurve is a simple continuously-compounded zero curve.
type FlatCurve struct {
	id       string
	currency Currency
	rate     decimal.Decimal
	anchor   time.Time
}

func NewFlatCurve(id string, currency Currency, rate decimal.Decimal, anchor time.Time) FlatCurve {
	return FlatCurve{
		id:       id,
		currency: currency,
		rate:     rate,
		anchor:   anchor,
	}
}

func (c FlatCurve) ID() string { return c.id }

func (c FlatCurve) DiscountFactor(ctx context.Context, currency Currency, date time.Time) (decimal.Decimal, error) {
	if err := ctx.Err(); err != nil {
		return decimal.Zero, err
	}
	if currency != c.currency {
		return decimal.Zero, errors.New("curve currency does not match contract currency")
	}
	if date.Before(c.anchor) {
		return decimal.Zero, ErrInvalidDate
	}

	years := decimal.NewFromFloat(date.Sub(c.anchor).Hours() / 24 / 365)
	rate, _ := c.rate.Float64()
	tau, _ := years.Float64()
	return decimal.NewFromFloat(math.Exp(-rate * tau)), nil
}

func (c FlatCurve) ForwardRate(ctx context.Context, currency Currency, resetDate, periodStart, periodEnd time.Time, dayCount DayCountConvention) (decimal.Decimal, error) {
	if err := ctx.Err(); err != nil {
		return decimal.Zero, err
	}
	if currency != c.currency {
		return decimal.Zero, errors.New("curve currency does not match contract currency")
	}
	if resetDate.After(periodStart) || periodEnd.Before(periodStart) {
		return decimal.Zero, ErrInvalidDate
	}
	if dayCount != DayCountActual360 && dayCount != DayCountActual365Fixed && dayCount != DayCountThirty360 && dayCount != DayCountActualActual {
		return decimal.Zero, ErrUnsupportedConvention
	}
	return c.rate, nil
}
