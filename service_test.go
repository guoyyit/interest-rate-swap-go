package irswap

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestContractValidation(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	valid := testContract("valid", start, maturity, FrequencyQuarterly, decimal.NewFromFloat(0.02), true)
	if err := svc.CreateContract(ctx, valid); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Contract)
	}{
		{
			name:   "maturity before start",
			mutate: func(c *Contract) { c.MaturityDate = c.StartDate.AddDate(0, 0, -1) },
		},
		{
			name:   "negative notional",
			mutate: func(c *Contract) { c.Notional = decimal.NewFromInt(-1) },
		},
		{
			name:   "unsupported day count",
			mutate: func(c *Contract) { c.FixedDayCount = DayCountConvention("bad") },
		},
		{
			name:   "missing calendar",
			mutate: func(c *Contract) { c.CalendarID = "" },
		},
		{
			name: "unsorted notional schedule",
			mutate: func(c *Contract) {
				c.NotionalSchedule = []NotionalChange{
					{Date: c.StartDate.AddDate(0, 6, 0), Notional: decimal.NewFromInt(500_000)},
					{Date: c.StartDate.AddDate(0, 3, 0), Notional: decimal.NewFromInt(700_000)},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := testContract("invalid-"+tt.name, start, maturity, FrequencyQuarterly, decimal.NewFromFloat(0.02), true)
			tt.mutate(contract)
			if err := svc.CreateContract(ctx, contract); err == nil {
				t.Fatal("CreateContract() expected error, got nil")
			}
		})
	}
}

func TestDayCountConventions(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		convention DayCountConvention
		want       decimal.Decimal
	}{
		{DayCountActual360, decimal.NewFromInt(91).Div(decimal.NewFromInt(360))},
		{DayCountActual365Fixed, decimal.NewFromInt(91).Div(decimal.NewFromInt(365))},
		{DayCountThirty360, decimal.NewFromInt(90).Div(decimal.NewFromInt(360))},
		{DayCountActualActual, decimal.NewFromInt(91).Div(decimal.NewFromInt(366))},
	}

	for _, tt := range tests {
		t.Run(string(tt.convention), func(t *testing.T) {
			got, err := yearFraction(start, end, tt.convention)
			if err != nil {
				t.Fatalf("yearFraction() error = %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("yearFraction() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCashflowScheduleAndBusinessDayAdjustment(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)     // Saturday
	maturity := time.Date(2025, 5, 31, 0, 0, 0, 0, time.UTC) // Saturday
	contract := testContract("schedule", start, maturity, FrequencyMonthly, decimal.NewFromFloat(0.02), true)
	contract.BusinessDayConvention = BusinessDayModifiedFollowing
	contract.FloatingDayCount = DayCountActual365Fixed
	if err := svc.CreateContract(ctx, contract); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}

	cashflows, err := svc.GenerateCashflows(ctx, contract.ID)
	if err != nil {
		t.Fatalf("GenerateCashflows() error = %v", err)
	}
	if len(cashflows) != 6 {
		t.Fatalf("len(cashflows) = %d, want 6", len(cashflows))
	}

	wantReset := time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC)
	if !cashflows[0].ResetDate.Equal(wantReset) {
		t.Fatalf("first reset date = %s, want %s", cashflows[0].ResetDate.Format("2006-01-02"), wantReset.Format("2006-01-02"))
	}
	wantPayment := time.Date(2025, 5, 30, 0, 0, 0, 0, time.UTC) // Modified following avoids June.
	if !cashflows[4].PaymentDate.Equal(wantPayment) {
		t.Fatalf("last payment date = %s, want %s", cashflows[4].PaymentDate.Format("2006-01-02"), wantPayment.Format("2006-01-02"))
	}

	// March has 31 days. 31/360 * 1,000,000 * 2% = 1,722.22.
	wantFixed := decimal.NewFromFloat(1722.22)
	if !cashflows[0].Amount.Equal(wantFixed) {
		t.Fatalf("first fixed amount = %s, want %s", cashflows[0].Amount, wantFixed)
	}
}

func TestNotionalSchedule(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	contract := testContract("notional", start, maturity, FrequencyMonthly, decimal.NewFromFloat(0.02), true)
	contract.NotionalSchedule = []NotionalChange{
		{Date: time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), Notional: decimal.NewFromInt(500_000)},
		{Date: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), Notional: decimal.NewFromInt(250_000)},
	}
	if err := svc.CreateContract(ctx, contract); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}

	cashflows, err := svc.GenerateCashflows(ctx, contract.ID)
	if err != nil {
		t.Fatalf("GenerateCashflows() error = %v", err)
	}
	wantNotionals := []decimal.Decimal{
		decimal.NewFromInt(1_000_000),
		decimal.NewFromInt(500_000),
		decimal.NewFromInt(250_000),
	}
	wantAmounts := []decimal.Decimal{
		decimal.NewFromFloat(1722.22),
		decimal.NewFromFloat(777.78),
		decimal.NewFromFloat(430.56),
	}
	for i := 0; i < 3; i++ {
		fixed := cashflows[i*2]
		if fixed.Leg != LegTypeFixed {
			t.Fatalf("cashflow %d leg = %s, want fixed", i, fixed.Leg)
		}
		if !fixed.Notional.Equal(wantNotionals[i]) {
			t.Fatalf("period %d notional = %s, want %s", i+1, fixed.Notional, wantNotionals[i])
		}
		if !fixed.Amount.Equal(wantAmounts[i]) {
			t.Fatalf("period %d amount = %s, want %s", i+1, fixed.Amount, wantAmounts[i])
		}
	}
}

func TestResetDeterminismAndNetSettlement(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	contract := testContract("settle", start, maturity, FrequencyAnnual, decimal.NewFromFloat(0.02), true)
	contract.FixedDayCount = DayCountThirty360
	contract.FloatingDayCount = DayCountThirty360
	if err := svc.CreateContract(ctx, contract); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}

	curve := NewFlatCurve("curve", "USD", decimal.NewFromFloat(0.025), start)
	reset, err := svc.ResetFloatingRate(ctx, contract.ID, start, curve)
	if err != nil {
		t.Fatalf("ResetFloatingRate() error = %v", err)
	}
	if !reset.Rate.Equal(decimal.NewFromFloat(0.026)) {
		t.Fatalf("reset rate = %s, want 0.026", reset.Rate)
	}

	// A different curve for the same reset date must not change the confirmed result.
	noisyCurve := NewFlatCurve("noisy", "USD", decimal.NewFromFloat(0.999), start)
	again, err := svc.ResetFloatingRate(ctx, contract.ID, start, noisyCurve)
	if err != nil {
		t.Fatalf("second ResetFloatingRate() error = %v", err)
	}
	if !again.Rate.Equal(reset.Rate) {
		t.Fatalf("reset rate changed from %s to %s", reset.Rate, again.Rate)
	}

	payment, err := svc.ConfirmPayment(ctx, contract.ID, start)
	if err != nil {
		t.Fatalf("ConfirmPayment() error = %v", err)
	}
	if !payment.FixedAmount.Equal(decimal.NewFromInt(20_000)) {
		t.Fatalf("fixed amount = %s, want 20000", payment.FixedAmount)
	}
	if !payment.FloatingAmount.Equal(decimal.NewFromInt(26_000)) {
		t.Fatalf("floating amount = %s, want 26000", payment.FloatingAmount)
	}
	if !payment.NetAmount.Equal(decimal.NewFromInt(-6_000)) {
		t.Fatalf("net amount = %s, want -6000", payment.NetAmount)
	}

	duplicate, err := svc.ConfirmPayment(ctx, contract.ID, start)
	if err != nil {
		t.Fatalf("duplicate ConfirmPayment() error = %v", err)
	}
	if duplicate.ID != payment.ID || !duplicate.NetAmount.Equal(payment.NetAmount) {
		t.Fatal("duplicate payment did not return the original confirmed payment")
	}

	net, err := svc.NetPayable(ctx, contract.ID)
	if err != nil {
		t.Fatalf("NetPayable() error = %v", err)
	}
	if !net.Equal(decimal.NewFromInt(-6_000)) {
		t.Fatalf("NetPayable() = %s, want -6000", net)
	}
}

func TestZeroFloatingRate(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	contract := testContract("zero-rate", start, maturity, FrequencyAnnual, decimal.Zero, true)
	contract.Spread = decimal.Zero
	contract.FixedDayCount = DayCountThirty360
	contract.FloatingDayCount = DayCountThirty360
	if err := svc.CreateContract(ctx, contract); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}
	curve := NewFlatCurve("zero", "USD", decimal.Zero, start)
	reset, err := svc.ResetFloatingRate(ctx, contract.ID, start, curve)
	if err != nil {
		t.Fatalf("ResetFloatingRate() error = %v", err)
	}
	if !reset.Rate.IsZero() {
		t.Fatalf("reset rate = %s, want zero", reset.Rate)
	}
	payment, err := svc.ConfirmPayment(ctx, contract.ID, start)
	if err != nil {
		t.Fatalf("ConfirmPayment() error = %v", err)
	}
	if !payment.NetAmount.IsZero() {
		t.Fatalf("net amount = %s, want zero", payment.NetAmount)
	}
	stored, err := svc.GetContract(ctx, contract.ID)
	if err != nil {
		t.Fatalf("GetContract() error = %v", err)
	}
	if stored.Status != ContractStatusMatured {
		t.Fatalf("status = %s, want matured", stored.Status)
	}
}

func TestTermination(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	contract := testContract("terminate", start, maturity, FrequencyAnnual, decimal.NewFromFloat(0.02), true)
	contract.FixedDayCount = DayCountThirty360
	contract.FloatingDayCount = DayCountThirty360
	if err := svc.CreateContract(ctx, contract); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}

	terminationDate := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	curve := NewFlatCurve("curve", "USD", decimal.NewFromFloat(0.02), terminationDate)
	termination, err := svc.Terminate(ctx, contract.ID, terminationDate, curve)
	if err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	want := decimal.NewFromFloat(-980.20)
	if !termination.Amount.Equal(want) {
		t.Fatalf("termination amount = %s, want %s", termination.Amount, want)
	}

	stored, err := svc.GetContract(ctx, contract.ID)
	if err != nil {
		t.Fatalf("GetContract() error = %v", err)
	}
	if stored.Status != ContractStatusTerminated {
		t.Fatalf("status = %s, want terminated", stored.Status)
	}

	again, err := svc.Terminate(ctx, contract.ID, terminationDate, curve)
	if err != nil {
		t.Fatalf("second Terminate() error = %v", err)
	}
	if !again.Amount.Equal(termination.Amount) {
		t.Fatal("second termination changed the recorded amount")
	}
}

func TestBatchValuationMatchesSingleContractAndDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	payer := testContract("payer", start, maturity, FrequencyAnnual, decimal.NewFromFloat(0.02), true)
	receiver := testContract("receiver", start, maturity, FrequencyAnnual, decimal.NewFromFloat(0.02), false)
	for _, contract := range []*Contract{payer, receiver} {
		contract.FixedDayCount = DayCountThirty360
		contract.FloatingDayCount = DayCountThirty360
		if err := svc.CreateContract(ctx, contract); err != nil {
			t.Fatalf("CreateContract(%s) error = %v", contract.ID, err)
		}
	}

	request := ValuationRequest{
		Curve:         NewFlatCurve("curve", "USD", decimal.NewFromFloat(0.02), start),
		ValuationDate: start,
	}
	all, err := svc.ValueAll(ctx, request)
	if err != nil {
		t.Fatalf("ValueAll() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2", len(all))
	}

	for i, contract := range []*Contract{payer, receiver} {
		single, err := svc.ValueContract(ctx, contract.ID, request)
		if err != nil {
			t.Fatalf("ValueContract(%s) error = %v", contract.ID, err)
		}
		if !all[i].NetPV.Equal(single.NetPV) {
			t.Fatalf("ValueAll NetPV = %s, single NetPV = %s", all[i].NetPV, single.NetPV)
		}
	}

	cashflows, err := svc.ListCashflows(ctx, payer.ID)
	if err != nil {
		t.Fatalf("ListCashflows() error = %v", err)
	}
	if len(cashflows) != 0 {
		t.Fatalf("valuation mutated state: generated %d cashflows", len(cashflows))
	}
}

func TestCrashRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewJSONStore(filepath.Join(dir, "state.json"))
	calendars := NewMapCalendarRegistry(NewWeekendCalendar("weekend"))
	svc, err := NewService(calendars, store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	contract := testContract("recover", start, maturity, FrequencyAnnual, decimal.NewFromFloat(0.02), true)
	contract.FixedDayCount = DayCountThirty360
	contract.FloatingDayCount = DayCountThirty360
	if err := svc.CreateContract(ctx, contract); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}
	curve := NewFlatCurve("curve", "USD", decimal.NewFromFloat(0.025), start)
	if _, err := svc.ResetFloatingRate(ctx, contract.ID, start, curve); err != nil {
		t.Fatalf("ResetFloatingRate() error = %v", err)
	}
	if _, err := svc.ConfirmPayment(ctx, contract.ID, start); err != nil {
		t.Fatalf("ConfirmPayment() error = %v", err)
	}

	recovered, err := NewService(calendars, store)
	if err != nil {
		t.Fatalf("NewService(recovered) error = %v", err)
	}
	stored, err := recovered.GetContract(ctx, contract.ID)
	if err != nil {
		t.Fatalf("GetContract() error = %v", err)
	}
	if stored.ID != contract.ID {
		t.Fatalf("recovered contract ID = %s, want %s", stored.ID, contract.ID)
	}

	resets, err := recovered.ListRateResets(ctx, contract.ID)
	if err != nil {
		t.Fatalf("ListRateResets() error = %v", err)
	}
	if len(resets) != 1 || !resets[0].Rate.Equal(decimal.NewFromFloat(0.026)) {
		t.Fatalf("recovered resets = %+v, want one 0.026 reset", resets)
	}

	payments, err := recovered.ListPayments(ctx, contract.ID)
	if err != nil {
		t.Fatalf("ListPayments() error = %v", err)
	}
	if len(payments) != 1 || !payments[0].NetAmount.Equal(decimal.NewFromInt(-6_000)) {
		t.Fatalf("recovered payments = %+v, want one -6000 payment", payments)
	}

	noisyCurve := NewFlatCurve("noisy", "USD", decimal.NewFromFloat(0.999), start)
	if _, err := recovered.ResetFloatingRate(ctx, contract.ID, start, noisyCurve); err != nil {
		t.Fatalf("post-restart ResetFloatingRate() error = %v", err)
	}
	resets, _ = recovered.ListRateResets(ctx, contract.ID)
	if len(resets) != 1 {
		t.Fatalf("post-restart reset count = %d, want 1", len(resets))
	}
	if _, err := recovered.ConfirmPayment(ctx, contract.ID, start); err != nil {
		t.Fatalf("post-restart ConfirmPayment() error = %v", err)
	}
	payments, _ = recovered.ListPayments(ctx, contract.ID)
	if len(payments) != 1 {
		t.Fatalf("post-restart payment count = %d, want 1", len(payments))
	}
}

func TestConcurrentSettlementAndReset(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	maturity := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	contract := testContract("concurrent", start, maturity, FrequencyQuarterly, decimal.NewFromFloat(0.02), true)
	contract.FixedDayCount = DayCountThirty360
	contract.FloatingDayCount = DayCountThirty360
	if err := svc.CreateContract(ctx, contract); err != nil {
		t.Fatalf("CreateContract() error = %v", err)
	}

	resetDates := []time.Time{
		start,
		start.AddDate(0, 3, 0),
		start.AddDate(0, 6, 0),
		start.AddDate(0, 9, 0),
	}
	curve := NewFlatCurve("curve", "USD", decimal.NewFromFloat(0.025), start)
	for _, resetDate := range resetDates {
		if _, err := svc.ResetFloatingRate(ctx, contract.ID, resetDate, curve); err != nil {
			t.Fatalf("ResetFloatingRate(%s) error = %v", resetDate.Format("2006-01-02"), err)
		}
	}

	var wg sync.WaitGroup
	for _, resetDate := range resetDates {
		wg.Add(1)
		go func(date time.Time) {
			defer wg.Done()
			if _, err := svc.ConfirmPayment(ctx, contract.ID, date); err != nil {
				t.Errorf("ConfirmPayment(%s) error = %v", date.Format("2006-01-02"), err)
			}
		}(resetDate)
	}
	wg.Wait()

	payments, err := svc.ListPayments(ctx, contract.ID)
	if err != nil {
		t.Fatalf("ListPayments() error = %v", err)
	}
	if len(payments) != 4 {
		t.Fatalf("payment count = %d, want 4", len(payments))
	}

	net, err := svc.NetPayable(ctx, contract.ID)
	if err != nil {
		t.Fatalf("NetPayable() error = %v", err)
	}
	if net.IsZero() {
		t.Fatal("NetPayable() = 0, want non-zero")
	}

	// Many concurrent resets on one date must create only one deterministic reset record.
	resetContract := testContract("concurrent-reset", start, maturity, FrequencyAnnual, decimal.NewFromFloat(0.02), true)
	if err := svc.CreateContract(ctx, resetContract); err != nil {
		t.Fatalf("CreateContract(reset contract) error = %v", err)
	}
	wg.Add(50)
	for i := 0; i < 50; i++ {
		go func() {
			defer wg.Done()
			if _, err := svc.ResetFloatingRate(ctx, resetContract.ID, start, curve); err != nil {
				t.Errorf("ResetFloatingRate() error = %v", err)
			}
		}()
	}
	wg.Wait()
	resets, err := svc.ListRateResets(ctx, resetContract.ID)
	if err != nil {
		t.Fatalf("ListRateResets() error = %v", err)
	}
	if len(resets) != 1 {
		t.Fatalf("reset count = %d, want 1", len(resets))
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store := NewJSONStore(filepath.Join(t.TempDir(), "state.json"))
	calendars := NewMapCalendarRegistry(NewWeekendCalendar("weekend"))
	svc, err := NewService(calendars, store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func testContract(id string, start, maturity time.Time, frequency Frequency, fixedRate decimal.Decimal, fixedPayer bool) *Contract {
	return &Contract{
		ID:                    id,
		Currency:              "USD",
		TradeDate:             start.AddDate(0, 0, -2),
		StartDate:             start,
		MaturityDate:          maturity,
		FixedRate:             fixedRate,
		FloatingIndex:         "SOFR",
		Spread:                decimal.NewFromFloat(0.001),
		FixedDayCount:         DayCountActual360,
		FloatingDayCount:      DayCountActual360,
		PaymentFrequency:      frequency,
		ResetFrequency:        frequency,
		BusinessDayConvention: BusinessDayFollowing,
		CalendarID:            "weekend",
		FixedPayer:            fixedPayer,
		Notional:              decimal.NewFromInt(1_000_000),
	}
}
