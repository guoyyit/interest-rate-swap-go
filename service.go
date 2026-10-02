package swap

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	// ErrContractNotFound is returned for unknown contract IDs.
	ErrContractNotFound = errors.New("contract not found")
	// ErrContractExists is returned when registering a duplicate ID.
	ErrContractExists = errors.New("contract already exists")
	// ErrNotActive is returned when operating on a terminated contract.
	ErrNotActive = errors.New("contract is not active")
)

// resetKey identifies one float-leg period fixing.
type resetKey struct {
	leg, period int
}

// contractState is the mutable in-memory state of one contract.
type contractState struct {
	spec      ContractSpec
	schedules [2]LegSchedule
	status    Status

	resets     map[resetKey]ResetPayload
	payments   []PaymentPayload
	paidItems  map[[2]int]bool
	cumulative Money

	termination *TerminationPayload
}

func newContractState(p ContractPayload) *contractState {
	return &contractState{
		spec:      p.Spec,
		schedules: p.Schedules,
		status:    Active,
		resets:    map[resetKey]ResetPayload{},
		paidItems: map[[2]int]bool{},
	}
}

// Service registers swap contracts and drives their lifecycle:
// rate fixings, payment confirmation, early termination, valuation
// and crash recovery. It is safe for concurrent use.
type Service struct {
	mu        sync.RWMutex
	cal       Calendar
	store     Store
	contracts map[string]*contractState
}

// Open creates a Service over store and replays any persisted
// journal, recovering the pre-crash state.
func Open(store Store, cal Calendar) (*Service, error) {
	s := &Service{
		cal:       cal,
		store:     store,
		contracts: map[string]*contractState{},
	}
	recs, err := store.Load()
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if err := s.apply(r); err != nil {
			return nil, fmt.Errorf("replay %v for %s: %w", r.Kind, r.ContractID, err)
		}
	}
	return s, nil
}

// apply folds one journal record into in-memory state. It is
// idempotent so replay and retries never double-apply.
func (s *Service) apply(r Record) error {
	switch r.Kind {
	case RecordContract:
		var p ContractPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		if _, ok := s.contracts[r.ContractID]; !ok {
			s.contracts[r.ContractID] = newContractState(p)
		}
	case RecordReset:
		var p ResetPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		cs, ok := s.contracts[r.ContractID]
		if !ok {
			return ErrContractNotFound
		}
		cs.resets[resetKey{p.LegIndex, p.PeriodIndex}] = p
	case RecordPayment:
		var p PaymentPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		cs, ok := s.contracts[r.ContractID]
		if !ok {
			return ErrContractNotFound
		}
		allPaid := len(p.Items) > 0
		for _, it := range p.Items {
			if !cs.paidItems[[2]int{it.LegIndex, it.PeriodIndex}] {
				allPaid = false
			}
		}
		if allPaid {
			return nil // duplicate record, already applied
		}
		for _, it := range p.Items {
			cs.paidItems[[2]int{it.LegIndex, it.PeriodIndex}] = true
		}
		cs.payments = append(cs.payments, p)
		cs.cumulative = p.Cumulative
	case RecordTermination:
		var p TerminationPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		cs, ok := s.contracts[r.ContractID]
		if !ok {
			return ErrContractNotFound
		}
		cs.status = Terminated
		tp := p
		cs.termination = &tp
	default:
		return fmt.Errorf("unknown record kind %d", int(r.Kind))
	}
	return nil
}

// append persists one record and folds it into memory. The record is
// durable before the in-memory state changes, so a crash in between
// is healed by replay.
func (s *Service) append(r Record) error {
	if err := s.store.Append(r); err != nil {
		return err
	}
	return s.apply(r)
}

// sortedIDs returns contract IDs in deterministic order.
func (s *Service) sortedIDs() []string {
	ids := make([]string, 0, len(s.contracts))
	for id := range s.contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// CreateContract validates and registers a new contract, returning
// its ID. Invalid specs are rejected and nothing is persisted.
func (s *Service) CreateContract(spec ContractSpec) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", fmt.Errorf("invalid contract: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.contracts[spec.ID]; ok {
		return "", ErrContractExists
	}
	var schedules [2]LegSchedule
	for i, leg := range spec.Legs {
		ls, err := BuildLegSchedule(spec.EffectiveDate, spec.MaturityDate, leg, s.cal)
		if err != nil {
			return "", fmt.Errorf("leg %d: %w", i, err)
		}
		schedules[i] = ls
	}
	payload, err := json.Marshal(ContractPayload{Spec: spec, Schedules: schedules})
	if err != nil {
		return "", err
	}
	if err := s.append(Record{Kind: RecordContract, ContractID: spec.ID, Payload: payload}); err != nil {
		return "", err
	}
	return spec.ID, nil
}

// FixRates confirms all pending float fixings with reset date on or
// before asOf, using curve. Already-confirmed fixings are skipped.
func (s *Service) FixRates(asOf Date, curve Curve) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.sortedIDs() {
		cs := s.contracts[id]
		if cs.status != Active {
			continue
		}
		for li, ls := range cs.schedules {
			if ls.Spec.RateType != FloatRateLeg {
				continue
			}
			for _, p := range ls.Periods {
				if p.ResetDate.After(asOf) {
					continue
				}
				key := resetKey{li, p.Index}
				if _, ok := cs.resets[key]; ok {
					continue // already fixed, never rewritten
				}
				dcf := ls.Spec.DayCount.Fraction(p.AccrualStart, p.AccrualEnd)
				rate := ForwardRate(curve, p.ResetDate, p.AccrualStart, p.AccrualEnd, dcf)
				payload, err := json.Marshal(ResetPayload{
					LegIndex:    li,
					PeriodIndex: p.Index,
					ResetDate:   p.ResetDate,
					Rate:        rate,
				})
				if err != nil {
					return err
				}
				if err := s.append(Record{Kind: RecordReset, ContractID: id, Payload: payload}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ConfirmPayments confirms all due payments (payment date on or
// before asOf) whose fixings are in place, computing per-leg
// amounts, the net and the running cumulative per contract.
func (s *Service) ConfirmPayments(asOf Date) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Phase 1: collect and price every due, unconfirmed item. Any
	// missing fixing aborts the whole run before anything persists.
	type dueItem struct {
		leg, period int
		amount      Money // signed
	}
	type contractDue struct {
		cs    *contractState
		byDay map[Date][]dueItem
		dates []Date
	}
	due := map[string]*contractDue{}
	for _, id := range s.sortedIDs() {
		cs := s.contracts[id]
		if cs.status != Active {
			continue
		}
		for li, ls := range cs.schedules {
			for _, p := range ls.Periods {
				if p.PaymentDate.After(asOf) || cs.paidItems[[2]int{li, p.Index}] {
					continue
				}
				rate := ls.Spec.FixedRate
				if ls.Spec.RateType == FloatRateLeg {
					fix, ok := cs.resets[resetKey{li, p.Index}]
					if !ok {
						return fmt.Errorf("contract %s leg %d period %d: missing rate fixing",
							id, li, p.Index)
					}
					rate = fix.Rate + ls.Spec.Spread
				}
				amount := legAmount(ls.Spec, p, rate)
				cd := due[id]
				if cd == nil {
					cd = &contractDue{cs: cs, byDay: map[Date][]dueItem{}}
					due[id] = cd
				}
				day := p.PaymentDate
				if _, ok := cd.byDay[day]; !ok {
					cd.dates = append(cd.dates, day)
				}
				cd.byDay[day] = append(cd.byDay[day], dueItem{li, p.Index, amount})
			}
		}
	}
	// Phase 2: persist one net payment record per contract per day.
	for _, id := range s.sortedIDs() {
		cd, ok := due[id]
		if !ok {
			continue
		}
		sort.Slice(cd.dates, func(i, j int) bool { return cd.dates[i].Before(cd.dates[j]) })
		for _, day := range cd.dates {
			items := cd.byDay[day]
			pp := PaymentPayload{PaymentDate: day}
			for _, it := range items {
				pp.Items = append(pp.Items, LegCashflow{
					LegIndex:    it.leg,
					PeriodIndex: it.period,
					Amount:      it.amount,
				})
				pp.Net += it.amount
			}
			pp.Cumulative = cd.cs.cumulative + pp.Net
			payload, err := json.Marshal(pp)
			if err != nil {
				return err
			}
			if err := s.append(Record{Kind: RecordPayment, ContractID: id, Payload: payload}); err != nil {
				return err
			}
		}
	}
	return nil
}

// legAmount computes one period's signed cashflow for a leg at rate.
func legAmount(spec LegSpec, p Period, rate float64) Money {
	dcf := spec.DayCount.Fraction(p.AccrualStart, p.AccrualEnd)
	gross := Money(mathRound(float64(p.Notional) * rate * dcf))
	return gross * spec.Direction.Sign()
}

// Terminate settles a contract early at asOf using curve and returns
// the signed settlement (positive: book receives). The settlement is
// atomic: it either persists fully or not at all.
func (s *Service) Terminate(id string, asOf Date, curve Curve) (Money, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := s.contracts[id]
	if !ok {
		return 0, ErrContractNotFound
	}
	if cs.status != Active {
		return 0, ErrNotActive
	}
	settlement := cs.mtm(asOf, curve)
	payload, err := json.Marshal(TerminationPayload{SettlementDate: asOf, Settlement: settlement})
	if err != nil {
		return 0, err
	}
	// The whole state transition is this single durable record.
	if err := s.append(Record{Kind: RecordTermination, ContractID: id, Payload: payload}); err != nil {
		return 0, err
	}
	return settlement, nil
}

// Value returns the mark-to-market of one contract at asOf without
// mutating any state.
func (s *Service) Value(id string, asOf Date, curve Curve) (Money, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.contracts[id]
	if !ok {
		return 0, ErrContractNotFound
	}
	if cs.status != Active {
		return 0, nil // no remaining cashflows after termination
	}
	return cs.mtm(asOf, curve), nil
}

// ValueAll returns the mark-to-market of every contract at asOf.
// Results equal per-contract Value calls; no state is modified.
func (s *Service) ValueAll(asOf Date, curve Curve) (map[string]Money, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Money, len(s.contracts))
	for _, id := range s.sortedIDs() {
		cs := s.contracts[id]
		if cs.status != Active {
			out[id] = 0
			continue
		}
		out[id] = cs.mtm(asOf, curve)
	}
	return out, nil
}

// mtm values the remaining net cashflows of a contract at asOf.
// Future payments use confirmed fixings where they exist and curve
// forwards otherwise. It is a pure function of state and curve.
func (cs *contractState) mtm(asOf Date, curve Curve) Money {
	var pv float64
	for li, ls := range cs.schedules {
		for _, p := range ls.Periods {
			if !p.PaymentDate.After(asOf) || cs.paidItems[[2]int{li, p.Index}] {
				continue
			}
			rate := ls.Spec.FixedRate
			if ls.Spec.RateType == FloatRateLeg {
				if fix, ok := cs.resets[resetKey{li, p.Index}]; ok {
					rate = fix.Rate + ls.Spec.Spread
				} else {
					dcf := ls.Spec.DayCount.Fraction(p.AccrualStart, p.AccrualEnd)
					rate = ForwardRate(curve, asOf, p.AccrualStart, p.AccrualEnd, dcf) + ls.Spec.Spread
				}
			}
			amount := legAmount(ls.Spec, p, rate)
			pv += float64(amount) * curve.DiscountFactor(asOf, p.PaymentDate)
		}
	}
	return Money(mathRound(pv))
}

// ContractView is an immutable snapshot of a contract's state.
type ContractView struct {
	Spec        ContractSpec
	Schedules   [2]LegSchedule
	Status      Status
	Resets      []ResetPayload
	Payments    []PaymentPayload
	Termination *TerminationPayload
	Cumulative  Money
}

// Contract returns an immutable view of one contract.
func (s *Service) Contract(id string) (ContractView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.contracts[id]
	if !ok {
		return ContractView{}, ErrContractNotFound
	}
	v := ContractView{
		Spec:       cs.spec,
		Status:     cs.status,
		Cumulative: cs.cumulative,
	}
	for i, ls := range cs.schedules {
		periods := make([]Period, len(ls.Periods))
		copy(periods, ls.Periods)
		v.Schedules[i] = LegSchedule{Spec: ls.Spec, Periods: periods}
	}
	for _, r := range cs.resets {
		v.Resets = append(v.Resets, r)
	}
	sort.Slice(v.Resets, func(i, j int) bool {
		if v.Resets[i].LegIndex != v.Resets[j].LegIndex {
			return v.Resets[i].LegIndex < v.Resets[j].LegIndex
		}
		return v.Resets[i].PeriodIndex < v.Resets[j].PeriodIndex
	})
	v.Payments = append(v.Payments, cs.payments...)
	if cs.termination != nil {
		t := *cs.termination
		v.Termination = &t
	}
	return v, nil
}

// Contracts lists the IDs of all registered contracts.
func (s *Service) Contracts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortedIDs()
}
