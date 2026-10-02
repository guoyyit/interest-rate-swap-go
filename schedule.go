package swap

import "errors"

// errInvalidSchedule reports a leg that cannot span its dates.
var errInvalidSchedule = errors.New("invalid schedule parameters")

// Period is one accrual period of a leg.
type Period struct {
	Index        int   `json:"index"`
	AccrualStart Date  `json:"accrual_start"` // unadjusted
	AccrualEnd   Date  `json:"accrual_end"`   // unadjusted
	PaymentDate  Date  `json:"payment_date"`  // adjusted accrual end
	ResetDate    Date  `json:"reset_date"`    // float legs only; zero for fixed
	Notional     Money `json:"notional"`      // notional in force for the period
}

// LegSchedule is a leg spec plus its generated periods.
type LegSchedule struct {
	Spec    LegSpec  `json:"spec"`
	Periods []Period `json:"periods"`
}

// BuildLegSchedule generates accrual periods for a leg spanning
// [effective, maturity). Accrual boundaries are unadjusted; payment
// and reset dates are business-day adjusted. A short final stub is
// allowed when maturity does not fall on a period boundary.
func BuildLegSchedule(effective, maturity Date, spec LegSpec, cal Calendar) (LegSchedule, error) {
	if !effective.Before(maturity) {
		return LegSchedule{}, errInvalidSchedule
	}
	months := spec.PayFreq.Months()
	if months <= 0 {
		return LegSchedule{}, errInvalidSchedule
	}
	ls := LegSchedule{Spec: spec}
	start := effective
	for start.Before(maturity) {
		end := start.AddDate(0, months, 0)
		if end.After(maturity) {
			end = maturity // short final stub
		}
		p := Period{
			Index:        len(ls.Periods),
			AccrualStart: start,
			AccrualEnd:   end,
			PaymentDate:  Adjust(cal, spec.BDC, end),
			Notional:     notionalAt(spec.Notional, start),
		}
		if spec.RateType == FloatRateLeg {
			adjStart := Adjust(cal, spec.BDC, start)
			p.ResetDate = AddBusinessDays(cal, adjStart, -spec.FixingLagDays)
		}
		ls.Periods = append(ls.Periods, p)
		start = end
	}
	return ls, nil
}

// notionalAt returns the notional in force at d from a step plan.
// The plan is validated to cover [effective, maturity), so a step
// always matches.
func notionalAt(steps []NotionalStep, d Date) Money {
	n := steps[0].Amount
	for _, s := range steps {
		if s.From.After(d) {
			break
		}
		n = s.Amount
	}
	return n
}
