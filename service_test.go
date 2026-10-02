package swap

import (
	"reflect"
	"strings"
	"testing"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := Open(NewMemoryStore(), NewWeekendCalendar())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// flatFloatSpec is validSpec with zero spread for hand-checkable math.
func flatFloatSpec() ContractSpec {
	spec := validSpec()
	spec.Legs[1].Spread = 0
	return spec
}

// expectedFloatAmount recomputes a float period amount from public
// helpers, independently of the service internals.
func expectedFloatAmount(t *testing.T, curve Curve, ls LegSchedule, p Period) Money {
	t.Helper()
	dcf := ls.Spec.DayCount.Fraction(p.AccrualStart, p.AccrualEnd)
	rate := ForwardRate(curve, p.ResetDate, p.AccrualStart, p.AccrualEnd, dcf)
	return Money(mathRound(float64(p.Notional) * rate * dcf))
}

func TestCreateContractRejectsInvalid(t *testing.T) {
	svc := newTestService(t)
	bad := validSpec()
	bad.MaturityDate = bad.EffectiveDate
	if _, err := svc.CreateContract(bad); err == nil {
		t.Fatal("invalid spec must be rejected")
	}
	if got := svc.Contracts(); len(got) != 0 {
		t.Fatalf("rejected contract registered: %v", got)
	}
	if _, err := svc.CreateContract(validSpec()); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if _, err := svc.CreateContract(validSpec()); err != ErrContractExists {
		t.Fatalf("duplicate id: got %v, want ErrContractExists", err)
	}
}

func TestServiceLifecycleNetting(t *testing.T) {
	svc := newTestService(t)
	spec := flatFloatSpec()
	id, err := svc.CreateContract(spec)
	if err != nil {
		t.Fatal(err)
	}
	curve := FlatCurve{Rate: 0.05}
	end := MustParseDate("2026-01-02")
	if err := svc.FixRates(end, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(end); err != nil {
		t.Fatal(err)
	}
	view, err := svc.Contract(id)
	if err != nil {
		t.Fatal(err)
	}
	// Two years: 8 quarterly float periods, 4 semiannual fixed periods.
	if len(view.Resets) != 8 {
		t.Fatalf("got %d resets, want 8", len(view.Resets))
	}
	floatSched := view.Schedules[1]
	fixedSched := view.Schedules[0]
	// Every reset rate is reproducible from the curve.
	for _, r := range view.Resets {
		p := floatSched.Periods[r.PeriodIndex]
		dcf := floatSched.Spec.DayCount.Fraction(p.AccrualStart, p.AccrualEnd)
		want := ForwardRate(curve, p.ResetDate, p.AccrualStart, p.AccrualEnd, dcf)
		if r.Rate != want {
			t.Errorf("period %d rate %v, want reproducible %v", r.PeriodIndex, r.Rate, want)
		}
	}
	// 8 payment events (fixed dates coincide with quarterly dates).
	if len(view.Payments) != 8 {
		t.Fatalf("got %d payments, want 8", len(view.Payments))
	}
	// Fixed coupon: 1e8 minor units * 3.5% * 0.5 (30/360) = 1.75e6.
	const fixedCoupon = Money(1_750_000)
	var cumulative Money
	for i, pay := range view.Payments {
		var net Money
		for _, it := range pay.Items {
			ls := view.Schedules[it.LegIndex]
			p := ls.Periods[it.PeriodIndex]
			var want Money
			if ls.Spec.RateType == FixedRateLeg {
				want = -fixedCoupon // pay leg
			} else {
				want = expectedFloatAmount(t, curve, ls, p) // receive leg
			}
			if it.Amount != want {
				t.Errorf("payment %d item %+v: amount %v, want %v", i, it, it.Amount, want)
			}
			net += it.Amount
		}
		if pay.Net != net {
			t.Errorf("payment %d net %v, want sum of items %v", i, pay.Net, net)
		}
		cumulative += net
		if pay.Cumulative != cumulative {
			t.Errorf("payment %d cumulative %v, want running sum %v", i, pay.Cumulative, cumulative)
		}
	}
	if view.Cumulative != cumulative {
		t.Errorf("view cumulative %v, want %v", view.Cumulative, cumulative)
	}
	// The July dates net both legs: 2 items each.
	for _, pay := range view.Payments {
		m := pay.PaymentDate.Month()
		if m == 7 || m == 1 {
			if len(pay.Items) != 2 {
				t.Errorf("payment %s: got %d items, want 2 (netting both legs)",
					pay.PaymentDate, len(pay.Items))
			}
		}
	}
	_ = fixedSched
}

func TestFixAndConfirmIdempotent(t *testing.T) {
	svc := newTestService(t)
	id, _ := svc.CreateContract(flatFloatSpec())
	curve := FlatCurve{Rate: 0.05}
	asOf := MustParseDate("2024-07-02")
	if err := svc.FixRates(asOf, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(asOf); err != nil {
		t.Fatal(err)
	}
	before, _ := svc.Contract(id)
	// Re-running must not duplicate or rewrite confirmed records.
	if err := svc.FixRates(asOf, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(asOf); err != nil {
		t.Fatal(err)
	}
	after, _ := svc.Contract(id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("re-running fix/confirm changed confirmed state")
	}
}

func TestConfirmedRecordsAreImmutable(t *testing.T) {
	svc := newTestService(t)
	id, _ := svc.CreateContract(flatFloatSpec())
	curve := FlatCurve{Rate: 0.05}
	first := MustParseDate("2024-07-02")
	if err := svc.FixRates(first, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(first); err != nil {
		t.Fatal(err)
	}
	early, _ := svc.Contract(id)
	// Advance the book to maturity.
	end := MustParseDate("2026-01-02")
	if err := svc.FixRates(end, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(end); err != nil {
		t.Fatal(err)
	}
	full, _ := svc.Contract(id)
	if len(full.Payments) <= len(early.Payments) {
		t.Fatalf("expected more payments later: %d vs %d", len(full.Payments), len(early.Payments))
	}
	// The confirmed prefix must be byte-for-byte identical.
	for i, p := range early.Payments {
		if !reflect.DeepEqual(p, full.Payments[i]) {
			t.Errorf("confirmed payment %d was rewritten", i)
		}
	}
	earlyResets := map[[2]int]float64{}
	for _, r := range early.Resets {
		earlyResets[[2]int{r.LegIndex, r.PeriodIndex}] = r.Rate
	}
	for _, r := range full.Resets {
		if rate, ok := earlyResets[[2]int{r.LegIndex, r.PeriodIndex}]; ok && rate != r.Rate {
			t.Errorf("confirmed reset for period %d changed: %v -> %v", r.PeriodIndex, rate, r.Rate)
		}
	}
}

func TestMissingFixingBlocksPayment(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.CreateContract(flatFloatSpec()); err != nil {
		t.Fatal(err)
	}
	err := svc.ConfirmPayments(MustParseDate("2024-04-02"))
	if err == nil || !strings.Contains(err.Error(), "missing rate fixing") {
		t.Fatalf("got %v, want missing-fixing error", err)
	}
	// Fixing the due reset unblocks confirmation.
	curve := FlatCurve{Rate: 0.05}
	if err := svc.FixRates(MustParseDate("2024-01-02"), curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(MustParseDate("2024-04-02")); err != nil {
		t.Fatal(err)
	}
}

func TestTerminate(t *testing.T) {
	svc := newTestService(t)
	id, _ := svc.CreateContract(flatFloatSpec())
	curve := FlatCurve{Rate: 0.05}
	mid := MustParseDate("2024-07-02")
	if err := svc.FixRates(mid, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(mid); err != nil {
		t.Fatal(err)
	}
	asOf := MustParseDate("2024-10-02")
	want, err := svc.Value(id, asOf, curve)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := svc.Terminate(id, asOf, curve)
	if err != nil {
		t.Fatal(err)
	}
	if settlement != want {
		t.Errorf("settlement %v, want pre-termination value %v", settlement, want)
	}
	view, _ := svc.Contract(id)
	if view.Status != Terminated {
		t.Errorf("status %v, want TERMINATED", view.Status)
	}
	if view.Termination == nil || view.Termination.Settlement != settlement ||
		!view.Termination.SettlementDate.Equal(asOf) {
		t.Errorf("termination record %+v", view.Termination)
	}
	resetsBefore := len(view.Resets)
	paymentsBefore := len(view.Payments)
	// A terminated contract is frozen: no more fixings or payments.
	end := MustParseDate("2026-01-02")
	if err := svc.FixRates(end, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(end); err != nil {
		t.Fatal(err)
	}
	view, _ = svc.Contract(id)
	if len(view.Resets) != resetsBefore || len(view.Payments) != paymentsBefore {
		t.Error("terminated contract gained records")
	}
	if _, err := svc.Terminate(id, asOf, curve); err != ErrNotActive {
		t.Errorf("re-terminate: got %v, want ErrNotActive", err)
	}
	if v, err := svc.Value(id, asOf, curve); err != nil || v != 0 {
		t.Errorf("terminated value %v, %v; want 0, nil", v, err)
	}
}

func TestValueAllMatchesPerContract(t *testing.T) {
	svc := newTestService(t)
	specs := []ContractSpec{flatFloatSpec()}
	// A monthly, amortizing, ACT/360 fixed payer swap.
	s2 := validSpec()
	s2.ID = "SWAP-2"
	s2.Legs[0].PayFreq = Monthly
	s2.Legs[0].DayCount = Act360
	s2.Legs[0].Notional = []NotionalStep{
		{From: MustParseDate("2024-01-02"), Amount: MoneyFromUnits(2_000_000)},
		{From: MustParseDate("2025-01-02"), Amount: MoneyFromUnits(1_000_000)},
	}
	specs = append(specs, s2)
	// An annual ACT/ACT swap with opposite directions.
	s3 := validSpec()
	s3.ID = "SWAP-3"
	s3.Legs[0].Direction, s3.Legs[1].Direction = Receive, Pay
	s3.Legs[0].PayFreq = Annual
	s3.Legs[0].DayCount = ActAct
	specs = append(specs, s3)
	for _, spec := range specs {
		if _, err := svc.CreateContract(spec); err != nil {
			t.Fatal(err)
		}
	}
	curve := FlatCurve{Rate: 0.04}
	asOf := MustParseDate("2024-10-02")
	if err := svc.FixRates(asOf, curve); err != nil {
		t.Fatal(err)
	}
	before := map[string]ContractView{}
	for _, id := range svc.Contracts() {
		v, _ := svc.Contract(id)
		before[id] = v
	}
	all, err := svc.ValueAll(asOf, curve)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(specs) {
		t.Fatalf("ValueAll returned %d results, want %d", len(all), len(specs))
	}
	for id, batch := range all {
		single, err := svc.Value(id, asOf, curve)
		if err != nil {
			t.Fatal(err)
		}
		if batch != single {
			t.Errorf("%s: batch %v != single %v", id, batch, single)
		}
	}
	// Valuation must not mutate any state.
	for id, prev := range before {
		now, _ := svc.Contract(id)
		if !reflect.DeepEqual(prev, now) {
			t.Errorf("%s: valuation mutated contract state", id)
		}
	}
}
