package irswap

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type Service struct {
	mu           sync.RWMutex
	calendars    CalendarRegistry
	contracts    map[string]*Contract
	cashflows    map[string][]Cashflow
	resets       map[string][]RateReset
	payments     map[string][]Payment
	terminations map[string]Termination
	store        Store
}

func NewService(calendars CalendarRegistry, store Store) (*Service, error) {
	s := &Service{
		calendars:    calendars,
		store:        store,
		contracts:    make(map[string]*Contract),
		cashflows:    make(map[string][]Cashflow),
		resets:       make(map[string][]RateReset),
		payments:     make(map[string][]Payment),
		terminations: make(map[string]Termination),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) ResetFloatingRate(ctx context.Context, contractID string, resetDate time.Time, curve MarketCurve) (*RateReset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if curve == nil {
		return nil, fmt.Errorf("%w: market curve is required", ErrInvalidContract)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	contract, ok := s.contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	calendar, err := s.calendarFor(contract.CalendarID)
	if err != nil {
		return nil, err
	}
	effectiveResetDate := AdjustDate(resetDate, calendar, contract.BusinessDayConvention)

	if len(s.cashflows[contractID]) == 0 {
		cashflows, err := s.generateCashflowsLocked(contract)
		if err != nil {
			return nil, err
		}
		s.cashflows[contractID] = cashflows
	}

	var targets []*Cashflow
	for i := range s.cashflows[contractID] {
		cashflow := &s.cashflows[contractID][i]
		if cashflow.Leg == LegTypeFloating && cashflow.ResetDate.Equal(effectiveResetDate) {
			targets = append(targets, cashflow)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: no floating reset on %s", ErrInvalidDate, effectiveResetDate.Format("2006-01-02"))
	}

	for _, reset := range s.resets[contractID] {
		if reset.ResetDate.Equal(effectiveResetDate) {
			stored := reset
			return &stored, nil
		}
	}
	if contract.Status != ContractStatusActive {
		return nil, fmt.Errorf("%w: contract %s is %s", ErrInvalidContract, contractID, contract.Status)
	}

	for _, cashflow := range targets {
		if cashflow.Confirmed {
			return nil, fmt.Errorf("%w: cashflow %s is already confirmed", ErrInvalidContract, cashflow.ID)
		}
		forward, err := curve.ForwardRate(ctx, contract.Currency, effectiveResetDate, cashflow.PeriodStart, cashflow.PeriodEnd, contract.FloatingDayCount)
		if err != nil {
			return nil, err
		}
		rate := forward.Add(contract.Spread)
		yf, err := yearFraction(cashflow.PeriodStart, cashflow.PeriodEnd, contract.FloatingDayCount)
		if err != nil {
			return nil, err
		}
		cashflow.Rate = rate
		cashflow.Amount = cashflow.Notional.Mul(rate).Mul(yf).Round(2)
		cashflow.ResetApplied = true
	}

	reset := RateReset{
		ContractID: contractID,
		ResetDate:  effectiveResetDate,
		Rate:       targets[0].Rate,
		CurveID:    curve.ID(),
		CreatedAt:  time.Now().UTC(),
	}
	s.resets[contractID] = append(s.resets[contractID], reset)
	if err := s.persistLocked(); err != nil {
		s.resets[contractID] = s.resets[contractID][:len(s.resets[contractID])-1]
		for _, cashflow := range targets {
			cashflow.Rate = decimal.Zero
			cashflow.Amount = decimal.Zero
		}
		return nil, err
	}
	stored := reset
	return &stored, nil
}

func (s *Service) ConfirmPayment(ctx context.Context, contractID string, periodStart time.Time) (*Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	contract, ok := s.contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	for _, payment := range s.payments[contractID] {
		if payment.PeriodStart.Equal(periodStart) {
			stored := payment
			return &stored, nil
		}
	}
	if contract.Status != ContractStatusActive {
		return nil, fmt.Errorf("%w: contract %s is %s", ErrInvalidContract, contractID, contract.Status)
	}

	if len(s.cashflows[contractID]) == 0 {
		cashflows, err := s.generateCashflowsLocked(contract)
		if err != nil {
			return nil, err
		}
		s.cashflows[contractID] = cashflows
	}

	var fixedLeg, floatingLeg *Cashflow
	for i := range s.cashflows[contractID] {
		cashflow := &s.cashflows[contractID][i]
		if !cashflow.PeriodStart.Equal(periodStart) {
			continue
		}
		if cashflow.Leg == LegTypeFixed {
			fixedLeg = cashflow
		} else {
			floatingLeg = cashflow
		}
	}
	if fixedLeg == nil || floatingLeg == nil {
		return nil, fmt.Errorf("%w: no payment period starting %s", ErrInvalidDate, periodStart.Format("2006-01-02"))
	}
	if !floatingLeg.ResetApplied {
		return nil, fmt.Errorf("%w: floating rate for %s is not reset", ErrInvalidContract, periodStart.Format("2006-01-02"))
	}
	if fixedLeg.Confirmed || floatingLeg.Confirmed {
		return nil, fmt.Errorf("%w: period %s is already confirmed", ErrInvalidContract, periodStart.Format("2006-01-02"))
	}

	net := fixedLeg.Amount.Sub(floatingLeg.Amount)
	if !contract.FixedPayer {
		net = floatingLeg.Amount.Sub(fixedLeg.Amount)
	}
	payment := Payment{
		ID:             fmt.Sprintf("%s-%s", contractID, periodStart.Format("2006-01-02")),
		ContractID:     contractID,
		PeriodStart:    periodStart,
		PeriodEnd:      fixedLeg.PeriodEnd,
		PaymentDate:    fixedLeg.PaymentDate,
		FixedAmount:    fixedLeg.Amount,
		FloatingAmount: floatingLeg.Amount,
		NetAmount:      net,
		Currency:       contract.Currency,
		ConfirmedAt:    time.Now().UTC(),
	}

	fixedLeg.Confirmed = true
	floatingLeg.Confirmed = true
	s.payments[contractID] = append(s.payments[contractID], payment)
	previousStatus := contract.Status
	if len(s.payments[contractID]) == len(s.cashflows[contractID])/2 {
		contract.Status = ContractStatusMatured
	}
	if err := s.persistLocked(); err != nil {
		fixedLeg.Confirmed = false
		floatingLeg.Confirmed = false
		s.payments[contractID] = s.payments[contractID][:len(s.payments[contractID])-1]
		contract.Status = previousStatus
		return nil, err
	}
	stored := payment
	return &stored, nil
}

func (s *Service) Terminate(ctx context.Context, contractID string, terminationDate time.Time, curve MarketCurve) (*Termination, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if curve == nil {
		return nil, fmt.Errorf("%w: market curve is required", ErrInvalidContract)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	contract, ok := s.contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	if contract.Status == ContractStatusTerminated {
		termination := s.terminations[contractID]
		if termination.Date.Equal(terminationDate) {
			stored := termination
			return &stored, nil
		}
		return nil, fmt.Errorf("%w: contract %s already terminated on %s", ErrInvalidContract, contractID, termination.Date.Format("2006-01-02"))
	}
	if contract.Status != ContractStatusActive {
		return nil, fmt.Errorf("%w: contract %s is %s", ErrInvalidContract, contractID, contract.Status)
	}
	if terminationDate.Before(contract.StartDate) || !terminationDate.Before(contract.MaturityDate) {
		return nil, fmt.Errorf("%w: termination date must be within the contract term", ErrInvalidDate)
	}

	if len(s.cashflows[contractID]) == 0 {
		cashflows, err := s.generateCashflowsLocked(contract)
		if err != nil {
			return nil, err
		}
		s.cashflows[contractID] = cashflows
	}

	fixedPV := decimal.Zero
	floatingPV := decimal.Zero
	for _, cashflow := range s.cashflows[contractID] {
		if cashflow.Confirmed || !cashflow.PaymentDate.After(terminationDate) {
			continue
		}
		df, err := curve.DiscountFactor(ctx, contract.Currency, cashflow.PaymentDate)
		if err != nil {
			return nil, err
		}
		if cashflow.Leg == LegTypeFixed {
			fixedPV = fixedPV.Add(cashflow.Amount.Mul(df))
			continue
		}

		amount := cashflow.Amount
		if !cashflow.ResetApplied {
			forward, err := curve.ForwardRate(ctx, contract.Currency, cashflow.ResetDate, cashflow.PeriodStart, cashflow.PeriodEnd, contract.FloatingDayCount)
			if err != nil {
				return nil, err
			}
			rate := forward.Add(contract.Spread)
			yf, err := yearFraction(cashflow.PeriodStart, cashflow.PeriodEnd, contract.FloatingDayCount)
			if err != nil {
				return nil, err
			}
			amount = cashflow.Notional.Mul(rate).Mul(yf).Round(2)
		}
		floatingPV = floatingPV.Add(amount.Mul(df))
	}

	fixedPV = fixedPV.Round(2)
	floatingPV = floatingPV.Round(2)
	net := fixedPV.Sub(floatingPV)
	if !contract.FixedPayer {
		net = floatingPV.Sub(fixedPV)
	}
	termination := Termination{
		ContractID: contractID,
		Date:       terminationDate,
		Amount:     net,
		Currency:   contract.Currency,
		CreatedAt:  time.Now().UTC(),
	}

	contract.Status = ContractStatusTerminated
	contract.UpdatedAt = time.Now().UTC()
	s.terminations[contractID] = termination
	if err := s.persistLocked(); err != nil {
		contract.Status = ContractStatusActive
		delete(s.terminations, contractID)
		return nil, err
	}
	stored := termination
	return &stored, nil
}

func (s *Service) ListCashflows(ctx context.Context, contractID string) ([]Cashflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.contracts[contractID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	cashflows := cloneCashflows(s.cashflows[contractID])
	sort.Slice(cashflows, func(i, j int) bool { return cashflows[i].ID < cashflows[j].ID })
	return cashflows, nil
}

func (s *Service) ListRateResets(ctx context.Context, contractID string) ([]RateReset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.contracts[contractID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	resets := append([]RateReset(nil), s.resets[contractID]...)
	sort.Slice(resets, func(i, j int) bool { return resets[i].ResetDate.Before(resets[j].ResetDate) })
	return resets, nil
}

func (s *Service) ListPayments(ctx context.Context, contractID string) ([]Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.contracts[contractID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	payments := append([]Payment(nil), s.payments[contractID]...)
	sort.Slice(payments, func(i, j int) bool { return payments[i].PaymentDate.Before(payments[j].PaymentDate) })
	return payments, nil
}

func (s *Service) GetTermination(ctx context.Context, contractID string) (*Termination, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	termination, ok := s.terminations[contractID]
	if !ok {
		return nil, fmt.Errorf("%w: no termination for %s", ErrInvalidContract, contractID)
	}
	stored := termination
	return &stored, nil
}

func (s *Service) NetPayable(ctx context.Context, contractID string) (decimal.Decimal, error) {
	if err := ctx.Err(); err != nil {
		return decimal.Zero, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.contracts[contractID]; !ok {
		return decimal.Zero, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	total := decimal.Zero
	for _, payment := range s.payments[contractID] {
		total = total.Add(payment.NetAmount)
	}
	return total, nil
}

func (s *Service) load() error {
	if s.store == nil {
		return nil
	}

	snapshot, err := s.store.Load(context.Background())
	if err != nil {
		return err
	}

	for i := range snapshot.Contracts {
		contract := snapshot.Contracts[i]
		if contract.Status == "" {
			contract.Status = ContractStatusActive
		}
		if _, exists := s.contracts[contract.ID]; exists {
			return fmt.Errorf("%w: duplicate contract %q in snapshot", ErrInvalidContract, contract.ID)
		}
		s.contracts[contract.ID] = &contract
	}
	for _, cashflow := range snapshot.Cashflows {
		s.cashflows[cashflow.ContractID] = append(s.cashflows[cashflow.ContractID], cashflow)
	}
	for _, reset := range snapshot.Resets {
		s.resets[reset.ContractID] = append(s.resets[reset.ContractID], reset)
	}
	for _, payment := range snapshot.Payments {
		s.payments[payment.ContractID] = append(s.payments[payment.ContractID], payment)
	}
	for _, termination := range snapshot.Terminations {
		s.terminations[termination.ContractID] = termination
	}
	return nil
}

func (s *Service) persistLocked() error {
	if s.store == nil {
		return fmt.Errorf("%w: store is not configured", ErrInvalidContract)
	}

	contractIDs := make([]string, 0, len(s.contracts))
	for id := range s.contracts {
		contractIDs = append(contractIDs, id)
	}
	sort.Strings(contractIDs)

	snapshot := Snapshot{Version: snapshotVersion}
	for _, id := range contractIDs {
		contract := cloneContract(s.contracts[id])
		snapshot.Contracts = append(snapshot.Contracts, contract)
		snapshot.Cashflows = append(snapshot.Cashflows, cloneCashflows(s.cashflows[id])...)
		snapshot.Resets = append(snapshot.Resets, append([]RateReset(nil), s.resets[id]...)...)
		snapshot.Payments = append(snapshot.Payments, append([]Payment(nil), s.payments[id]...)...)
		if termination, ok := s.terminations[id]; ok {
			snapshot.Terminations = append(snapshot.Terminations, termination)
		}
	}

	sort.Slice(snapshot.Resets, func(i, j int) bool {
		if !snapshot.Resets[i].ResetDate.Equal(snapshot.Resets[j].ResetDate) {
			return snapshot.Resets[i].ResetDate.Before(snapshot.Resets[j].ResetDate)
		}
		return snapshot.Resets[i].CurveID < snapshot.Resets[j].CurveID
	})
	sort.Slice(snapshot.Payments, func(i, j int) bool {
		if !snapshot.Payments[i].PaymentDate.Equal(snapshot.Payments[j].PaymentDate) {
			return snapshot.Payments[i].PaymentDate.Before(snapshot.Payments[j].PaymentDate)
		}
		return snapshot.Payments[i].ID < snapshot.Payments[j].ID
	})
	sort.Slice(snapshot.Terminations, func(i, j int) bool {
		if !snapshot.Terminations[i].Date.Equal(snapshot.Terminations[j].Date) {
			return snapshot.Terminations[i].Date.Before(snapshot.Terminations[j].Date)
		}
		return snapshot.Terminations[i].ContractID < snapshot.Terminations[j].ContractID
	})
	return s.store.Save(context.Background(), snapshot)
}

func (s *Service) calendarFor(id string) (BusinessCalendar, error) {
	if id == "" {
		return nil, nil
	}
	if s.calendars == nil {
		return nil, fmt.Errorf("%w: calendar registry is not configured", ErrInvalidContract)
	}
	calendar, err := s.calendars.Get(id)
	if err != nil {
		return nil, err
	}
	if calendar == nil {
		return nil, fmt.Errorf("%w: calendar %q not found", ErrInvalidContract, id)
	}
	return calendar, nil
}

func yearFraction(start, end time.Time, convention DayCountConvention) (decimal.Decimal, error) {
	if end.Before(start) {
		return decimal.Zero, ErrInvalidDate
	}
	if start.Equal(end) {
		return decimal.Zero, nil
	}

	day := 24 * time.Hour
	switch convention {
	case DayCountActual360:
		days := int64(end.Sub(start) / day)
		return decimal.NewFromInt(days).Div(decimal.NewFromInt(360)), nil
	case DayCountActual365Fixed:
		days := int64(end.Sub(start) / day)
		return decimal.NewFromInt(days).Div(decimal.NewFromInt(365)), nil
	case DayCountThirty360:
		d1 := start.Day()
		d2 := end.Day()
		if d1 > 30 {
			d1 = 30
		}
		if d2 > 30 && d1 == 30 {
			d2 = 30
		}
		days := 360*(end.Year()-start.Year()) + 30*(int(end.Month())-int(start.Month())) + (d2 - d1)
		return decimal.NewFromInt(int64(days)).Div(decimal.NewFromInt(360)), nil
	case DayCountActualActual:
		days := decimal.Zero
		current := start
		for current.Year() < end.Year() {
			yearEnd := time.Date(current.Year()+1, 1, 1, 0, 0, 0, 0, current.Location())
			yearDays := int64(yearEnd.Sub(current).Hours() / 24)
			days = days.Add(decimal.NewFromInt(yearDays).Div(decimal.NewFromInt(daysInYear(current.Year()))))
			current = yearEnd
		}
		yearDays := int64(end.Sub(current).Hours() / 24)
		days = days.Add(decimal.NewFromInt(yearDays).Div(decimal.NewFromInt(daysInYear(end.Year()))))
		return days, nil
	default:
		return decimal.Zero, ErrUnsupportedConvention
	}
}

func isLeapYear(year int) bool {
	return year%400 == 0 || (year%4 == 0 && year%100 != 0)
}

func daysInYear(year int) int64 {
	if isLeapYear(year) {
		return 366
	}
	return 365
}
