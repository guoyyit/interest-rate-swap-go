package swap

import "testing"

// validSpec returns a plain-vanilla fixed-vs-float swap spec.
func validSpec() ContractSpec {
	return ContractSpec{
		ID:            "SWAP-1",
		EffectiveDate: MustParseDate("2024-01-02"),
		MaturityDate:  MustParseDate("2026-01-02"),
		Legs: [2]LegSpec{
			{
				Direction: Pay,
				RateType:  FixedRateLeg,
				FixedRate: 0.035,
				DayCount:  Thirty360,
				PayFreq:   Semiannual,
				BDC:       ModifiedFollowing,
				Notional:  []NotionalStep{{From: MustParseDate("2024-01-02"), Amount: MoneyFromUnits(1_000_000)}},
			},
			{
				Direction: Receive,
				RateType:  FloatRateLeg,
				Spread:    0.001,
				DayCount:  Act360,
				PayFreq:   Quarterly,
				BDC:       ModifiedFollowing,
				Notional:  []NotionalStep{{From: MustParseDate("2024-01-02"), Amount: MoneyFromUnits(1_000_000)}},
			},
		},
	}
}

func TestValidateAcceptsValidSpec(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestValidateRejectsBadSpecs(t *testing.T) {
	cases := map[string]func(*ContractSpec){
		"empty id": func(s *ContractSpec) { s.ID = "" },
		"effective after maturity": func(s *ContractSpec) {
			s.EffectiveDate, s.MaturityDate = s.MaturityDate, s.EffectiveDate
		},
		"effective equals maturity": func(s *ContractSpec) {
			s.MaturityDate = s.EffectiveDate
		},
		"same direction legs": func(s *ContractSpec) { s.Legs[1].Direction = Pay },
		"negative fixed rate": func(s *ContractSpec) { s.Legs[0].FixedRate = -0.01 },
		"negative fixing lag": func(s *ContractSpec) { s.Legs[1].FixingLagDays = -1 },
		"empty notional plan": func(s *ContractSpec) { s.Legs[0].Notional = nil },
		"first step not at effective": func(s *ContractSpec) {
			s.Legs[0].Notional[0].From = MustParseDate("2024-02-02")
		},
		"zero notional":     func(s *ContractSpec) { s.Legs[0].Notional[0].Amount = 0 },
		"negative notional": func(s *ContractSpec) { s.Legs[0].Notional[0].Amount = -100 },
		"non-increasing steps": func(s *ContractSpec) {
			s.Legs[0].Notional = append(s.Legs[0].Notional,
				NotionalStep{From: MustParseDate("2024-01-02"), Amount: MoneyFromUnits(500_000)})
		},
		"step at maturity": func(s *ContractSpec) {
			s.Legs[0].Notional = append(s.Legs[0].Notional,
				NotionalStep{From: MustParseDate("2026-01-02"), Amount: MoneyFromUnits(500_000)})
		},
		"invalid day count": func(s *ContractSpec) { s.Legs[0].DayCount = DayCount(99) },
		"invalid frequency": func(s *ContractSpec) { s.Legs[0].PayFreq = Frequency(99) },
		"invalid bdc":       func(s *ContractSpec) { s.Legs[0].BDC = BusinessDayConvention(99) },
		"invalid direction": func(s *ContractSpec) { s.Legs[0].Direction = Direction(99) },
		"invalid rate type": func(s *ContractSpec) { s.Legs[0].RateType = RateType(99) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validSpec()
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Errorf("expected rejection, got nil")
			}
		})
	}
}

func TestValidateAcceptsAmortizingPlan(t *testing.T) {
	spec := validSpec()
	spec.Legs[0].Notional = []NotionalStep{
		{From: MustParseDate("2024-01-02"), Amount: MoneyFromUnits(1_000_000)},
		{From: MustParseDate("2025-01-02"), Amount: MoneyFromUnits(500_000)},
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("amortizing plan rejected: %v", err)
	}
}
