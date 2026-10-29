package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"supplier-catalog-sync/internal/domain"
	"testing"
	"time"
)

func TestCommitSurvivesReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "catalog.json")
	s, _ := Open(p)
	n := s.State().Clone()
	n.Products["X"] = domain.Product{SKU: "X"}
	if err := s.Commit(n); err != nil {
		t.Fatal(err)
	}
	again, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.State().Products["X"]; !ok {
		t.Fatal("product missing")
	}
}
func TestCloneIndependent(t *testing.T) {
	s := emptyState()
	s.Products["X"] = domain.Product{SKU: "X", Name: "old"}
	finishedAt := time.Now()
	s.Runs = []domain.Run{{ID: 1, FinishedAt: &finishedAt}}
	s.Issues = []domain.Issue{{Raw: json.RawMessage(`{"sku":"X"}`)}}
	before := domain.Product{SKU: "X", Name: "before"}
	s.Changes = []domain.Change{{Before: &before}}
	c := s.Clone()
	p := c.Products["X"]
	p.Name = "new"
	c.Products["X"] = p
	*c.Runs[0].FinishedAt = time.Time{}
	c.Issues[0].Raw[0] = '['
	c.Changes[0].Before.Name = "changed"
	if s.Products["X"].Name != "old" {
		t.Fatal("clone changed original")
	}
	if s.Runs[0].FinishedAt.IsZero() || s.Issues[0].Raw[0] != '{' || s.Changes[0].Before.Name != "before" {
		t.Fatal("nested clone data changed original")
	}
}
func TestCorruptFileNotOverwritten(t *testing.T) {
	p := filepath.Join(t.TempDir(), "catalog.json")
	bad := []byte("not json")
	if err := os.WriteFile(p, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("wanted error")
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(bad) {
		t.Fatal("corrupt file changed")
	}
}
