package swap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// RecordKind identifies the type of a persisted event.
type RecordKind int

const (
	// RecordContract registers a new contract with its schedule.
	RecordContract RecordKind = iota
	// RecordReset confirms a floating-rate fixing for one period.
	RecordReset
	// RecordPayment confirms a net payment for one payment date.
	RecordPayment
	// RecordTermination confirms an early-termination settlement.
	RecordTermination
)

// Record is one append-only journal entry.
type Record struct {
	Kind       RecordKind      `json:"kind"`
	ContractID string          `json:"contract_id"`
	Payload    json.RawMessage `json:"payload"`
}

// ContractPayload persists a contract spec and generated schedules.
type ContractPayload struct {
	Spec      ContractSpec   `json:"spec"`
	Schedules [2]LegSchedule `json:"schedules"`
}

// ResetPayload persists one confirmed floating-rate fixing.
type ResetPayload struct {
	LegIndex    int     `json:"leg_index"`
	PeriodIndex int     `json:"period_index"`
	ResetDate   Date    `json:"reset_date"`
	Rate        float64 `json:"rate"`
}

// LegCashflow is one leg's confirmed cashflow inside a payment.
type LegCashflow struct {
	LegIndex    int   `json:"leg_index"`
	PeriodIndex int   `json:"period_index"`
	Amount      Money `json:"amount"` // signed: receive positive
}

// PaymentPayload persists one confirmed net payment event.
type PaymentPayload struct {
	PaymentDate Date          `json:"payment_date"`
	Items       []LegCashflow `json:"items"`
	Net         Money         `json:"net"`
	Cumulative  Money         `json:"cumulative"`
}

// TerminationPayload persists an early-termination settlement.
type TerminationPayload struct {
	SettlementDate Date  `json:"settlement_date"`
	Settlement     Money `json:"settlement"` // signed: receive positive
}

// Store is the append-only journal backing the service.
type Store interface {
	// Append durably writes one record before returning.
	Append(r Record) error
	// Load returns all records in append order.
	Load() ([]Record, error)
}

// MemoryStore is an in-memory Store, mainly for tests.
type MemoryStore struct {
	mu   sync.Mutex
	recs []Record
}

// NewMemoryStore builds an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// Append implements Store.
func (m *MemoryStore) Append(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
	return nil
}

// Load implements Store.
func (m *MemoryStore) Load() ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, len(m.recs))
	copy(out, m.recs)
	return out, nil
}

// FileStore is a Store backed by an append-only JSON-lines file,
// fsynced on every append.
type FileStore struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// NewFileStore opens (creating if needed) a journal file.
func NewFileStore(path string) (*FileStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileStore{path: path, f: f}, nil
}

// Append implements Store.
func (f *FileStore) Append(r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.f.Write(b); err != nil {
		return err
	}
	return f.f.Sync()
}

// Load implements Store.
func (f *FileStore) Load() ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.f.Seek(0, 0); err != nil {
		return nil, err
	}
	var out []Record
	sc := bufio.NewScanner(f.f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var lines [][]byte
	for sc.Scan() {
		b := append([]byte(nil), sc.Bytes()...)
		lines = append(lines, b)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for i, b := range lines {
		if len(b) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			// A crash can leave a torn final line: it was never
			// fsynced, so it is safe to ignore. Corruption anywhere
			// else is a hard error.
			if i == len(lines)-1 {
				break
			}
			return nil, fmt.Errorf("journal %s line %d: %w", f.path, i+1, err)
		}
		out = append(out, r)
	}
	// Rewind to end so later appends keep working.
	if _, err := f.f.Seek(0, 2); err != nil {
		return nil, err
	}
	return out, nil
}

// Close releases the underlying file handle.
func (f *FileStore) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.f.Close()
}
