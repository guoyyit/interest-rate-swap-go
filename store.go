package irswap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const snapshotVersion = 1

type Snapshot struct {
	Version      int
	Contracts    []Contract
	Cashflows    []Cashflow
	Resets       []RateReset
	Payments     []Payment
	Terminations []Termination
}

type Store interface {
	Load(ctx context.Context) (Snapshot, error)
	Save(ctx context.Context, snapshot Snapshot) error
}

type JSONStore struct {
	path string
}

func NewJSONStore(path string) *JSONStore {
	return &JSONStore{path: path}
}

func (s *JSONStore) Load(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Version: snapshotVersion}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Version == 0 {
		snapshot.Version = snapshotVersion
	}
	return snapshot, nil
}

func (s *JSONStore) Save(ctx context.Context, snapshot Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if snapshot.Version == 0 {
		snapshot.Version = snapshotVersion
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".swap-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

var _ Store = (*JSONStore)(nil)
