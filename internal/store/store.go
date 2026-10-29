package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"supplier-catalog-sync/internal/domain"
)

var ErrConcurrentUpdate = errors.New("catalog changed in another request; retry")

type State struct {
	Products map[string]domain.Product `json:"products"`
	Runs     []domain.Run              `json:"runs"`
	Issues   []domain.Issue            `json:"issues"`
	Changes  []domain.Change           `json:"changes"`
}

func emptyState() *State { return &State{Products: make(map[string]domain.Product)} }

func (s *State) Clone() *State {
	clone := &State{
		Products: make(map[string]domain.Product, len(s.Products)),
		Runs:     append([]domain.Run(nil), s.Runs...),
		Issues:   append([]domain.Issue(nil), s.Issues...),
		Changes:  append([]domain.Change(nil), s.Changes...),
	}
	for sku, product := range s.Products {
		clone.Products[sku] = product
	}
	for i := range clone.Runs {
		if s.Runs[i].FinishedAt != nil {
			finishedAt := *s.Runs[i].FinishedAt
			clone.Runs[i].FinishedAt = &finishedAt
		}
	}
	for i := range clone.Issues {
		clone.Issues[i].Raw = append(json.RawMessage(nil), s.Issues[i].Raw...)
	}
	for i := range clone.Changes {
		if s.Changes[i].Before != nil {
			before := *s.Changes[i].Before
			clone.Changes[i].Before = &before
		}
	}
	return clone
}

func (s *State) NextRunID() int {
	max := 0
	for _, r := range s.Runs {
		if r.ID > max {
			max = r.ID
		}
	}
	return max + 1
}
func (s *State) RunByID(id int) *domain.Run {
	for i := range s.Runs {
		if s.Runs[i].ID == id {
			return &s.Runs[i]
		}
	}
	return nil
}
func (s *State) OpenRun() *domain.Run {
	for i := len(s.Runs) - 1; i >= 0; i-- {
		if s.Runs[i].IsOpen() {
			return &s.Runs[i]
		}
	}
	return nil
}
func (s *State) LatestRun() *domain.Run {
	if len(s.Runs) == 0 {
		return nil
	}
	return &s.Runs[len(s.Runs)-1]
}

type Store struct {
	path  string
	state *State
}

func Open(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{path: path, state: emptyState()}, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	if state.Products == nil {
		state.Products = make(map[string]domain.Product)
	}
	return &Store{path: path, state: &state}, nil
}

func (s *Store) State() *State { return s.state }

// Commit leaves the file and in-memory state unchanged if any step through the
// atomic rename fails. Once rename succeeds, next is the committed state. The
// following directory sync is best-effort because the rename has already selected
// the new file and returning an error would leave disk and memory inconsistent.
func (s *Store) Commit(next *State) error {
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Rename is atomic: a crash leaves the old or new JSON file, never half a transaction.
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	ok = true
	s.state = next
	d, err := os.Open(dir)
	if err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
