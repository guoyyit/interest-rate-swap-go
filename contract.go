package irswap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrInvalidContract       = errors.New("invalid contract")
	ErrContractNotFound      = errors.New("contract not found")
	ErrInvalidDate           = errors.New("invalid date")
	ErrInvalidRate           = errors.New("invalid rate")
	ErrInvalidNotional       = errors.New("invalid notional")
	ErrUnsupportedConvention = errors.New("unsupported convention")
)

func (s *Service) CreateContract(ctx context.Context, contract *Contract) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if contract == nil {
		return fmt.Errorf("%w: contract is required", ErrInvalidContract)
	}
	if err := validateContract(contract); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.contracts[contract.ID]; exists {
		return fmt.Errorf("%w: contract %q already exists", ErrInvalidContract, contract.ID)
	}

	now := time.Now().UTC()
	stored := cloneContract(contract)
	stored.Status = ContractStatusActive
	stored.CreatedAt = now
	stored.UpdatedAt = now
	s.contracts[stored.ID] = &stored
	s.cashflows[stored.ID] = nil
	s.resets[stored.ID] = nil
	s.payments[stored.ID] = nil
	return s.persistLocked()
}

func (s *Service) GetContract(ctx context.Context, id string) (*Contract, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	contract, ok := s.contracts[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, id)
	}
	stored := cloneContract(contract)
	return &stored, nil
}

func (s *Service) ListContracts(ctx context.Context) ([]*Contract, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.contracts))
	for id := range s.contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	result := make([]*Contract, 0, len(ids))
	for _, id := range ids {
		contract := cloneContract(s.contracts[id])
		result = append(result, &contract)
	}
	return result, nil
}

func (s *Service) GenerateCashflows(ctx context.Context, contractID string) ([]Cashflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	contract, ok := s.contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	if contract.Status != ContractStatusActive {
		return nil, fmt.Errorf("%w: contract %s is %s", ErrInvalidContract, contractID, contract.Status)
	}
	if len(s.cashflows[contractID]) > 0 {
		return cloneCashflows(s.cashflows[contractID]), nil
	}

	cashflows, err := s.generateCashflowsLocked(contract)
	if err != nil {
		return nil, err
	}

	s.cashflows[contractID] = cashflows
	if err := s.persistLocked(); err != nil {
		s.cashflows[contractID] = nil
		return nil, err
	}
	return cloneCashflows(cashflows), nil
}

func (s *Service) generateCashflowsLocked(contract *Contract) ([]Cashflow, error) {
	calendar, err := s.calendarFor(contract.CalendarID)
	if err != nil {
		return nil, err
	}

	periods := paymentPeriods(contract.StartDate, contract.MaturityDate, contract.PaymentFrequency)
	cashflows := make([]Cashflow, 0, len(periods)*2)
	for i, period := range periods {
		notional := notionalAt(contract.Notional, contract.NotionalSchedule, period.start)
		resetDate := AdjustDate(period.start, calendar, contract.BusinessDayConvention)
		paymentDate := AdjustDate(period.end, calendar, contract.BusinessDayConvention)

		fixedYF, err := yearFraction(period.start, period.end, contract.FixedDayCount)
		if err != nil {
			return nil, err
		}
		fixedAmount := notional.Mul(contract.FixedRate).Mul(fixedYF).Round(2)

		floatingRate := decimal.Zero
		floatingResetApplied := false
		for _, reset := range s.resets[contract.ID] {
			if reset.ResetDate.Equal(resetDate) {
				floatingRate = reset.Rate
				floatingResetApplied = true
				break
			}
		}
		floatingYF, err := yearFraction(period.start, period.end, contract.FloatingDayCount)
		if err != nil {
			return nil, err
		}
		floatingAmount := decimal.Zero
		if floatingResetApplied {
			floatingAmount = notional.Mul(floatingRate).Mul(floatingYF).Round(2)
		}

		cashflows = append(cashflows,
			Cashflow{
				ID:          fmt.Sprintf("%s-fixed-%03d", contract.ID, i+1),
				ContractID:  contract.ID,
				Leg:         LegTypeFixed,
				PeriodStart: period.start,
				PeriodEnd:   period.end,
				ResetDate:   resetDate,
				PaymentDate: paymentDate,
				Rate:        contract.FixedRate,
				Notional:    notional,
				Amount:      fixedAmount,
				Currency:    contract.Currency,
			},
			Cashflow{
				ID:           fmt.Sprintf("%s-floating-%03d", contract.ID, i+1),
				ContractID:   contract.ID,
				Leg:          LegTypeFloating,
				PeriodStart:  period.start,
				PeriodEnd:    period.end,
				ResetDate:    resetDate,
				PaymentDate:  paymentDate,
				Rate:         floatingRate,
				Notional:     notional,
				Amount:       floatingAmount,
				Currency:     contract.Currency,
				ResetApplied: floatingResetApplied,
			},
		)
	}
	return cashflows, nil
}

type paymentPeriod struct {
	start time.Time
	end   time.Time
}

func paymentPeriods(start, maturity time.Time, frequency Frequency) []paymentPeriod {
	if frequency == FrequencyZero {
		return []paymentPeriod{{start: start, end: maturity}}
	}

	var periods []paymentPeriod
	for i := 1; ; i++ {
		end := AddPeriods(start, frequency, i)
		if !end.Before(maturity) {
			end = maturity
		}
		periods = append(periods, paymentPeriod{start: AddPeriods(start, frequency, i-1), end: end})
		if end.Equal(maturity) {
			return periods
		}
	}
}

func notionalAt(base decimal.Decimal, schedule []NotionalChange, date time.Time) decimal.Decimal {
	notional := base
	for _, change := range schedule {
		if !change.Date.After(date) {
			notional = change.Notional
		}
	}
	return notional
}

func validateContract(contract *Contract) error {
	if contract.ID == "" {
		return fmt.Errorf("%w: contract id is required", ErrInvalidContract)
	}
	if contract.Currency == "" {
		return fmt.Errorf("%w: currency is required", ErrInvalidContract)
	}
	if contract.StartDate.Before(contract.TradeDate) {
		return fmt.Errorf("%w: start date is before trade date", ErrInvalidDate)
	}
	if !contract.MaturityDate.After(contract.StartDate) {
		return fmt.Errorf("%w: maturity date must be after start date", ErrInvalidDate)
	}
	if contract.Notional.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("%w: notional must be positive", ErrInvalidNotional)
	}
	if contract.FixedRate.LessThanOrEqual(decimal.NewFromInt(-1)) {
		return fmt.Errorf("%w: fixed rate must be greater than -100%%", ErrInvalidRate)
	}
	if !validDayCount(contract.FixedDayCount) || !validDayCount(contract.FloatingDayCount) {
		return fmt.Errorf("%w: unsupported day count", ErrUnsupportedConvention)
	}
	if !validBusinessConvention(contract.BusinessDayConvention) {
		return fmt.Errorf("%w: unsupported business day convention", ErrUnsupportedConvention)
	}
	if !validPaymentFrequency(contract.PaymentFrequency) {
		return fmt.Errorf("%w: unsupported payment frequency", ErrUnsupportedConvention)
	}
	if contract.ResetFrequency != contract.PaymentFrequency && contract.ResetFrequency != FrequencyZero {
		return fmt.Errorf("%w: reset frequency must match payment frequency", ErrInvalidContract)
	}
	if contract.BusinessDayConvention != BusinessDayUnadjusted && contract.CalendarID == "" {
		return fmt.Errorf("%w: calendar is required for business day adjustment", ErrInvalidContract)
	}

	seen := make(map[time.Time]struct{})
	last := contract.StartDate
	for _, change := range contract.NotionalSchedule {
		if change.Date.Before(contract.StartDate) || !change.Date.Before(contract.MaturityDate) {
			return fmt.Errorf("%w: notional change date is outside contract period", ErrInvalidDate)
		}
		if _, ok := seen[change.Date]; ok {
			return fmt.Errorf("%w: duplicate notional change date", ErrInvalidDate)
		}
		if change.Date.Before(last) {
			return fmt.Errorf("%w: notional schedule must be sorted", ErrInvalidDate)
		}
		if change.Notional.LessThanOrEqual(decimal.Zero) {
			return fmt.Errorf("%w: notional change must be positive", ErrInvalidNotional)
		}
		seen[change.Date] = struct{}{}
		last = change.Date
	}
	return nil
}

func validDayCount(convention DayCountConvention) bool {
	switch convention {
	case DayCountActual360, DayCountActual365Fixed, DayCountThirty360, DayCountActualActual:
		return true
	default:
		return false
	}
}

func validBusinessConvention(convention BusinessDayConvention) bool {
	switch convention {
	case BusinessDayFollowing, BusinessDayModifiedFollowing, BusinessDayPreceding, BusinessDayUnadjusted:
		return true
	default:
		return false
	}
}

func validPaymentFrequency(frequency Frequency) bool {
	switch frequency {
	case FrequencyMonthly, FrequencyQuarterly, FrequencySemiannual, FrequencyAnnual, FrequencyZero:
		return true
	default:
		return false
	}
}

func cloneContract(contract *Contract) Contract {
	clone := *contract
	if contract.NotionalSchedule != nil {
		clone.NotionalSchedule = append([]NotionalChange(nil), contract.NotionalSchedule...)
	}
	return clone
}

func cloneCashflows(cashflows []Cashflow) []Cashflow {
	return append([]Cashflow(nil), cashflows...)
}
