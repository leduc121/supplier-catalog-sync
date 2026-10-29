package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/supplier"
	"testing"
)

func feed(t *testing.T, name string) *supplier.FileClient {
	t.Helper()
	c, err := supplier.OpenFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func engine(t *testing.T) (*Engine, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "catalog.json")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: s}, p
}
func start(t *testing.T, e *Engine, name string) (domain.Run, error) {
	t.Helper()
	return e.Start(context.Background(), name, feed(t, name))
}

func TestHappyPathCounters(t *testing.T) {
	e, _ := engine(t)
	r, err := start(t, e, "happy.json")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != domain.RunCompleted || r.Counters.Created != 4 || r.Counters.Updated != 1 || r.Counters.Withdrawn != 1 {
		t.Fatalf("bad run: %+v", r)
	}
	if len(e.Store.State().Products) != 4 || e.Store.State().Products["A1"].PriceCents != 12000 || e.Store.State().Products["A2"].Status != "withdrawn" {
		t.Fatal("bad catalog")
	}
}
func TestSameUpdateTwice(t *testing.T) {
	e, _ := engine(t)
	if _, err := start(t, e, "happy.json"); err != nil {
		t.Fatal(err)
	}
	before := e.Store.State().Products
	r, err := start(t, e, "happy.json")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.Store.State().Products) {
		t.Fatal("catalog changed")
	}
	if r.Counters.Created+r.Counters.Updated+r.Counters.Withdrawn+r.Counters.Relisted != 0 || r.Counters.Unchanged != 4 || r.Counters.StaleIgnored != 2 {
		t.Fatalf("bad counters: %+v", r.Counters)
	}
}
func TestOutOfOrder(t *testing.T) {
	e, _ := engine(t)
	if _, err := start(t, e, "dup_out_of_order.json"); err != nil {
		t.Fatal(err)
	}
	p := e.Store.State().Products
	if len(p) != 3 || p["B1"].Status != "withdrawn" || p["B1"].Revision != 3 || p["B2"].PriceCents != 5000 {
		t.Fatalf("bad catalog: %+v", p)
	}
}
func TestOutageResumeEqualsClean(t *testing.T) {
	e, _ := engine(t)
	c := feed(t, "happy.json")
	c.FailAfter = 2
	r, err := e.Start(context.Background(), "happy.json", c)
	if !errors.Is(err, ErrIncomplete) || r.BatchesDone() != 2 {
		t.Fatalf("run=%+v err=%v", r, err)
	}
	r, err = e.Resume(context.Background(), feed(t, "happy.json"))
	if err != nil {
		t.Fatal(err)
	}
	clean, _ := engine(t)
	want, _ := start(t, clean, "happy.json")
	if !reflect.DeepEqual(e.Store.State().Products, clean.Store.State().Products) || !reflect.DeepEqual(r.Counters, want.Counters) {
		t.Fatal("resumed result differs")
	}
}
func TestImmediateOutageKeepsCatalog(t *testing.T) {
	e, _ := engine(t)
	if _, err := start(t, e, "happy.json"); err != nil {
		t.Fatal(err)
	}
	before := e.Store.State().Products
	c := feed(t, "dup_out_of_order.json")
	c.FailAfter = 0
	if _, err := e.Start(context.Background(), "dup_out_of_order.json", c); !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.Store.State().Products) {
		t.Fatal("catalog changed")
	}
}
func TestCrashCanResume(t *testing.T) {
	e, path := engine(t)
	e.AfterBatch = func(seq int) {
		if seq == 2 {
			panic("crash")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_, _ = start(t, e, "happy.json")
	}()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e2 := &Engine{Store: s}
	if r := s.State().OpenRun(); r == nil || r.Status != "running" || r.BatchesDone() != 2 {
		t.Fatalf("bad durable run: %+v", r)
	}
	if _, err = e2.Start(context.Background(), "happy.json", feed(t, "happy.json")); !errors.Is(err, ErrRunOpen) {
		t.Fatal(err)
	}
	if _, err = e2.Resume(context.Background(), feed(t, "happy.json")); err != nil {
		t.Fatal(err)
	}
	clean, _ := engine(t)
	_, _ = start(t, clean, "happy.json")
	if !reflect.DeepEqual(s.State().Products, clean.Store.State().Products) {
		t.Fatal("crash recovery differs")
	}
}
func TestMissingBatchNeverCompleted(t *testing.T) {
	e, _ := engine(t)
	r, err := start(t, e, "missing_batch.json")
	if !errors.Is(err, ErrIncomplete) || r.Status != domain.RunInterrupted || r.BatchesDone() != 2 {
		t.Fatalf("run=%+v err=%v", r, err)
	}
}
func TestBadRecordsBecomeIssues(t *testing.T) {
	e, _ := engine(t)
	r, err := start(t, e, "bad_records.json")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != domain.RunCompletedWithIssues || r.Counters.IssueCount != 4 || len(e.Store.State().Products) != 2 || e.Store.State().Products["C1"].Name != "Belt" {
		t.Fatalf("bad result: %+v", r)
	}
}

type cancelClient struct {
	base   supplier.Client
	cancel context.CancelFunc
}

func (c cancelClient) TotalBatches(ctx context.Context) (int, error) { return c.base.TotalBatches(ctx) }
func (c cancelClient) FetchBatch(ctx context.Context, seq int) (domain.Batch, error) {
	b, e := c.base.FetchBatch(ctx, seq)
	if seq == 1 {
		c.cancel()
	}
	return b, e
}
func TestCancelledContextCanResume(t *testing.T) {
	e, _ := engine(t)
	ctx, cancel := context.WithCancel(context.Background())
	r, err := e.Start(ctx, "happy.json", cancelClient{feed(t, "happy.json"), cancel})
	if !errors.Is(err, ErrIncomplete) || r.BatchesDone() != 1 {
		t.Fatalf("run=%+v err=%v", r, err)
	}
	if _, err = e.Resume(context.Background(), feed(t, "happy.json")); err != nil {
		t.Fatal(err)
	}
}
func TestAbortKeepsWorkAndAllowsNewRun(t *testing.T) {
	e, _ := engine(t)
	c := feed(t, "happy.json")
	c.FailAfter = 1
	if _, err := e.Start(context.Background(), "happy.json", c); !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
	before := e.Store.State().Products
	r, err := e.Abort()
	if err != nil || r.Status != domain.RunAborted {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.Store.State().Products) {
		t.Fatal("abort changed products")
	}
	if _, err = start(t, e, "dup_out_of_order.json"); err != nil {
		t.Fatal(err)
	}
}
