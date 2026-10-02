package swap

import "fmt"

// Money is an amount in the single ledger currency, expressed in
// minor units (1/100 of a currency unit) to keep arithmetic exact.
type Money int64

// MoneyFromUnits converts major units (e.g. dollars) to Money.
func MoneyFromUnits(v float64) Money {
	return Money(mathRound(v * 100))
}

// Units returns the amount in major currency units.
func (m Money) Units() float64 { return float64(m) / 100 }

func (m Money) String() string { return fmt.Sprintf("%.2f", m.Units()) }

// mathRound rounds half away from zero; isolated for determinism.
func mathRound(v float64) int64 {
	if v < 0 {
		return -mathRound(-v)
	}
	return int64(v + 0.5)
}

// Direction is the side of a leg from the book's perspective.
type Direction int

const (
	// Pay means the book pays this leg's cashflows.
	Pay Direction = iota
	// Receive means the book receives this leg's cashflows.
	Receive
)

func (d Direction) valid() bool { return d == Pay || d == Receive }

// Sign returns +1 for Receive and -1 for Pay.
func (d Direction) Sign() Money {
	if d == Receive {
		return 1
	}
	return -1
}

// RateType distinguishes fixed from floating legs.
type RateType int

const (
	// FixedRateLeg pays a contractual fixed rate.
	FixedRateLeg RateType = iota
	// FloatRateLeg pays a reset rate plus spread.
	FloatRateLeg
)

func (rt RateType) valid() bool { return rt == FixedRateLeg || rt == FloatRateLeg }

// Frequency is a payment/accrual frequency.
type Frequency int

const (
	Monthly Frequency = iota
	Quarterly
	Semiannual
	Annual
)

// Months returns the number of months per period.
func (f Frequency) Months() int {
	switch f {
	case Monthly:
		return 1
	case Quarterly:
		return 3
	case Semiannual:
		return 6
	case Annual:
		return 12
	default:
		return 0
	}
}

func (f Frequency) valid() bool { return f.Months() > 0 }

// NotionalStep sets the notional from a date onward (piecewise
// constant amortization or accretion plan).
type NotionalStep struct {
	From   Date  `json:"from"`
	Amount Money `json:"amount"`
}

// LegSpec describes one leg of a swap.
type LegSpec struct {
	Direction Direction             `json:"direction"`
	RateType  RateType              `json:"rate_type"`
	FixedRate float64               `json:"fixed_rate"` // FixedRateLeg only, e.g. 0.035
	Spread    float64               `json:"spread"`     // FloatRateLeg only, added to fixing
	DayCount  DayCount              `json:"day_count"`
	PayFreq   Frequency             `json:"pay_freq"`
	BDC       BusinessDayConvention `json:"bdc"`
	// FixingLagDays is the number of business days the reset date
	// precedes the (adjusted) period start; FloatRateLeg only.
	FixingLagDays int            `json:"fixing_lag_days"`
	Notional      []NotionalStep `json:"notional"`
}

// ContractSpec describes a full interest-rate swap contract.
type ContractSpec struct {
	ID            string     `json:"id"`
	EffectiveDate Date       `json:"effective_date"`
	MaturityDate  Date       `json:"maturity_date"`
	Legs          [2]LegSpec `json:"legs"`
}

// Validate checks the spec for structural and parameter legality.
func (s ContractSpec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("contract id must not be empty")
	}
	if s.EffectiveDate.IsZero() || s.MaturityDate.IsZero() {
		return fmt.Errorf("effective and maturity dates must be set")
	}
	if !s.EffectiveDate.Before(s.MaturityDate) {
		return fmt.Errorf("effective date %s must be before maturity date %s",
			s.EffectiveDate, s.MaturityDate)
	}
	for i, leg := range s.Legs {
		if err := leg.validate(s); err != nil {
			return fmt.Errorf("leg %d: %w", i, err)
		}
	}
	if s.Legs[0].Direction == s.Legs[1].Direction {
		return fmt.Errorf("legs must have opposite directions")
	}
	return nil
}

// validate checks one leg against the contract-level dates.
func (l LegSpec) validate(s ContractSpec) error {
	if !l.Direction.valid() {
		return fmt.Errorf("invalid direction")
	}
	if !l.RateType.valid() {
		return fmt.Errorf("invalid rate type")
	}
	if !l.DayCount.valid() {
		return fmt.Errorf("invalid day count convention")
	}
	if !l.PayFreq.valid() {
		return fmt.Errorf("invalid payment frequency")
	}
	if !l.BDC.valid() {
		return fmt.Errorf("invalid business day convention")
	}
	switch l.RateType {
	case FixedRateLeg:
		if l.FixedRate < 0 {
			return fmt.Errorf("fixed rate must not be negative")
		}
	case FloatRateLeg:
		if l.FixingLagDays < 0 {
			return fmt.Errorf("fixing lag must not be negative")
		}
	}
	if len(l.Notional) == 0 {
		return fmt.Errorf("notional plan must not be empty")
	}
	if !l.Notional[0].From.Equal(s.EffectiveDate) {
		return fmt.Errorf("first notional step must start at effective date %s",
			s.EffectiveDate)
	}
	for i, step := range l.Notional {
		if step.Amount <= 0 {
			return fmt.Errorf("notional step %d must be positive", i)
		}
		if !step.From.Before(s.MaturityDate) {
			return fmt.Errorf("notional step %d starts at or after maturity", i)
		}
		if i > 0 && !step.From.After(l.Notional[i-1].From) {
			return fmt.Errorf("notional steps must be strictly increasing")
		}
	}
	return nil
}

// Status is the lifecycle state of a contract.
type Status int

const (
	// Active means the contract is live.
	Active Status = iota
	// Terminated means the contract was terminated early and settled.
	Terminated
)

func (st Status) String() string {
	switch st {
	case Active:
		return "ACTIVE"
	case Terminated:
		return "TERMINATED"
	default:
		return fmt.Sprintf("Status(%d)", int(st))
	}
}
