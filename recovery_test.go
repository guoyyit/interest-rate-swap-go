package swap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// flakyStore fails the next failNext appends, then works normally.
type flakyStore struct {
	inner    *MemoryStore
	mu       sync.Mutex
	failNext int
}

func newFlakyStore() *flakyStore { return &flakyStore{inner: NewMemoryStore()} }

func (f *flakyStore) Append(r Record) error {
	f.mu.Lock()
	if f.failNext > 0 {
		f.failNext--
		f.mu.Unlock()
		return errors.New("injected store failure")
	}
	f.mu.Unlock()
	return f.inner.Append(r)
}

func (f *flakyStore) Load() ([]Record, error) { return f.inner.Load() }

func (f *flakyStore) fail(n int) {
	f.mu.Lock()
	f.failNext = n
	f.mu.Unlock()
}

func openOn(t *testing.T, store Store) *Service {
	t.Helper()
	svc, err := Open(store, NewWeekendCalendar())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestFileStoreRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := openOn(t, fs)
	curve := FlatCurve{Rate: 0.05}
	id1, _ := svc.CreateContract(flatFloatSpec())
	s2 := validSpec()
	s2.ID = "SWAP-2"
	id2, _ := svc.CreateContract(s2)
	mid := MustParseDate("2024-07-02")
	if err := svc.FixRates(mid, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(mid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Terminate(id2, mid, curve); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the full pre-crash state must come back.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := openOn(t, fs2)
	if got := svc2.Contracts(); !reflect.DeepEqual(got, []string{"SWAP-1", "SWAP-2"}) {
		t.Fatalf("recovered contracts %v", got)
	}
	v1, _ := svc2.Contract(id1)
	if v1.Status != Active || len(v1.Payments) != 2 || len(v1.Resets) != 3 {
		t.Errorf("recovered SWAP-1: status %v, %d payments, %d resets",
			v1.Status, len(v1.Payments), len(v1.Resets))
	}
	v2, _ := svc2.Contract(id2)
	if v2.Status != Terminated || v2.Termination == nil {
		t.Errorf("recovered SWAP-2: status %v, termination %+v", v2.Status, v2.Termination)
	}
	// Processing continues seamlessly after recovery.
	end := MustParseDate("2026-01-02")
	if err := svc2.FixRates(end, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc2.ConfirmPayments(end); err != nil {
		t.Fatal(err)
	}
	v1, _ = svc2.Contract(id1)
	if len(v1.Payments) != 8 || len(v1.Resets) != 8 {
		t.Errorf("post-recovery SWAP-1: %d payments, %d resets", len(v1.Payments), len(v1.Resets))
	}
	v2, _ = svc2.Contract(id2)
	if len(v2.Payments) != 2 {
		t.Errorf("terminated SWAP-2 gained payments: %d", len(v2.Payments))
	}
	if err := fs2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryIgnoresTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := openOn(t, fs)
	id, _ := svc.CreateContract(flatFloatSpec())
	if err := svc.FixRates(MustParseDate("2024-01-02"), FlatCurve{Rate: 0.05}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: a torn final line without newline.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":1,"contract_id":"SWAP-1","payload":{"leg_`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := openOn(t, fs2)
	v, err := svc2.Contract(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Resets) != 1 {
		t.Errorf("recovered %d resets, want 1 (torn tail ignored)", len(v.Resets))
	}
	fs2.Close()
}

func TestDuplicateRecordsNotReapplied(t *testing.T) {
	// A journal containing a duplicated payment record (e.g. written
	// twice by a retried writer) must not double-apply on replay.
	store := NewMemoryStore()
	svc := openOn(t, store)
	id, _ := svc.CreateContract(flatFloatSpec())
	curve := FlatCurve{Rate: 0.05}
	asOf := MustParseDate("2024-04-02")
	if err := svc.FixRates(asOf, curve); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPayments(asOf); err != nil {
		t.Fatal(err)
	}
	recs, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Append a copy of the payment record and reopen.
	for _, r := range recs {
		if r.Kind == RecordPayment {
			if err := store.Append(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	svc2 := openOn(t, store)
	v1, _ := svc.Contract(id)
	v2, _ := svc2.Contract(id)
	if !reflect.DeepEqual(v1, v2) {
		t.Error("duplicate payment record changed recovered state")
	}
}

func TestTerminationAtomicity(t *testing.T) {
	store := newFlakyStore()
	svc := openOn(t, store)
	id, _ := svc.CreateContract(flatFloatSpec())
	curve := FlatCurve{Rate: 0.05}
	asOf := MustParseDate("2024-10-02")
	store.fail(1) // the termination append fails
	if _, err := svc.Terminate(id, asOf, curve); err == nil {
		t.Fatal("expected store failure")
	}
	view, _ := svc.Contract(id)
	if view.Status != Active || view.Termination != nil {
		t.Fatal("failed termination left partial state")
	}
	recs, _ := store.Load()
	for _, r := range recs {
		if r.Kind == RecordTermination {
			t.Fatal("failed termination persisted a record")
		}
	}
	// Retrying settles exactly once.
	settlement, err := svc.Terminate(id, asOf, curve)
	if err != nil {
		t.Fatal(err)
	}
	view, _ = svc.Contract(id)
	if view.Status != Terminated || view.Termination.Settlement != settlement {
		t.Fatal("retry did not settle cleanly")
	}
	count := 0
	recs, _ = store.Load()
	for _, r := range recs {
		if r.Kind == RecordTermination {
			count++
		}
	}
	if count != 1 {
		t.Errorf("got %d termination records, want exactly 1", count)
	}
}

func TestPaymentAtomicity(t *testing.T) {
	store := newFlakyStore()
	svc := openOn(t, store)
	id, _ := svc.CreateContract(flatFloatSpec())
	curve := FlatCurve{Rate: 0.05}
	asOf := MustParseDate("2024-07-02")
	if err := svc.FixRates(asOf, curve); err != nil {
		t.Fatal(err)
	}
	store.fail(1) // first payment append fails
	if err := svc.ConfirmPayments(asOf); err == nil {
		t.Fatal("expected store failure")
	}
	view, _ := svc.Contract(id)
	if len(view.Payments) != 0 || view.Cumulative != 0 {
		t.Fatal("failed confirmation left partial state")
	}
	// Retry confirms everything exactly once.
	if err := svc.ConfirmPayments(asOf); err != nil {
		t.Fatal(err)
	}
	view, _ = svc.Contract(id)
	if len(view.Payments) != 2 {
		t.Fatalf("got %d payments, want 2", len(view.Payments))
	}
	// Recovery agrees with the in-memory view.
	svc2 := openOn(t, store)
	view2, _ := svc2.Contract(id)
	if !reflect.DeepEqual(view, view2) {
		t.Error("recovered state differs after payment failure/retry")
	}
}

func TestConcurrentOperations(t *testing.T) {
	svc := newTestService(t)
	curve := FlatCurve{Rate: 0.05}
	const n = 8
	dates := []Date{
		MustParseDate("2024-04-02"), MustParseDate("2024-07-02"),
		MustParseDate("2024-10-02"), MustParseDate("2025-01-02"),
		MustParseDate("2025-04-02"), MustParseDate("2025-07-02"),
		MustParseDate("2025-10-02"), MustParseDate("2026-01-02"),
	}
	errCh := make(chan error, 4*n*len(dates))
	var wg sync.WaitGroup
	// Concurrent creation of distinct contracts.
	for g := 0; g < n; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			spec := flatFloatSpec()
			spec.ID = fmt.Sprintf("SWAP-%d", g)
			if _, err := svc.CreateContract(spec); err != nil {
				errCh <- err
			}
		}(g)
	}
	wg.Wait()
	// Concurrent lifecycle: fix, confirm, value, terminate.
	for g := 0; g < n; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := fmt.Sprintf("SWAP-%d", g)
			for _, d := range dates {
				if err := svc.FixRates(d, curve); err != nil {
					errCh <- fmt.Errorf("%s fix %s: %w", id, d, err)
					return
				}
				if err := svc.ConfirmPayments(d); err != nil {
					errCh <- fmt.Errorf("%s confirm %s: %w", id, d, err)
					return
				}
				if _, err := svc.Value(id, d, curve); err != nil {
					errCh <- fmt.Errorf("%s value %s: %w", id, d, err)
					return
				}
				if _, err := svc.ValueAll(d, curve); err != nil {
					errCh <- fmt.Errorf("%s valueAll %s: %w", id, d, err)
					return
				}
			}
			if g%2 == 0 {
				if _, err := svc.Terminate(id, dates[len(dates)-1], curve); err != nil {
					errCh <- fmt.Errorf("%s terminate: %w", id, err)
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	// Final state sanity: all contracts present, half terminated.
	ids := svc.Contracts()
	if len(ids) != n {
		t.Fatalf("got %d contracts, want %d", len(ids), n)
	}
	terminated := 0
	for _, id := range ids {
		v, _ := svc.Contract(id)
		if v.Status == Terminated {
			terminated++
			continue
		}
		if len(v.Payments) != 8 {
			t.Errorf("%s: got %d payments, want 8", id, len(v.Payments))
		}
	}
	if terminated != n/2 {
		t.Errorf("got %d terminated, want %d", terminated, n/2)
	}
}

func TestConcurrentTerminateSameContract(t *testing.T) {
	svc := newTestService(t)
	id, _ := svc.CreateContract(flatFloatSpec())
	curve := FlatCurve{Rate: 0.05}
	asOf := MustParseDate("2024-10-02")
	const racers = 4
	var wg sync.WaitGroup
	results := make([]error, racers)
	settlements := make([]Money, racers)
	for g := 0; g < racers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			settlements[g], results[g] = svc.Terminate(id, asOf, curve)
		}(g)
	}
	wg.Wait()
	wins := 0
	var winning Money
	for g := 0; g < racers; g++ {
		if results[g] == nil {
			wins++
			winning = settlements[g]
		} else if results[g] != ErrNotActive {
			t.Errorf("unexpected error: %v", results[g])
		}
	}
	if wins != 1 {
		t.Errorf("got %d successful terminations, want exactly 1", wins)
	}
	view, _ := svc.Contract(id)
	if view.Termination == nil || view.Termination.Settlement != winning {
		t.Error("recorded settlement does not match the winning racer")
	}
}
