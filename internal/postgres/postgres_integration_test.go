package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/postgres"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/supplier"
	"supplier-catalog-sync/internal/syncer"
	"sync"
	"testing"
	"time"
)

func database(t *testing.T) *postgres.Backend {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	if err := postgres.Migrate(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	backend, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	return backend
}

func session(t *testing.T, backend *postgres.Backend) syncer.Repository {
	t.Helper()
	st, err := backend.OpenSession(context.Background(), fmt.Sprintf("test-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func feed(t *testing.T, name string) *supplier.FileClient {
	t.Helper()
	client, err := supplier.OpenFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPostgresOutageResumeAcrossInstances(t *testing.T) {
	backend := database(t)
	id := fmt.Sprintf("resume-%d", time.Now().UnixNano())
	first, _ := backend.OpenSession(context.Background(), id)
	client := feed(t, "happy.json")
	client.FailAfter = 2
	engine := &syncer.Engine{Store: first}
	if run, more, err := engine.StartStep(context.Background(), "happy", client); err != nil || !more || run.BatchesDone() != 1 {
		t.Fatalf("first step run=%+v more=%v err=%v", run, more, err)
	}
	if run, more, err := engine.ContinueStep(context.Background(), client); err != nil || !more || run.BatchesDone() != 2 {
		t.Fatalf("second step run=%+v more=%v err=%v", run, more, err)
	}
	if run, _, err := engine.ContinueStep(context.Background(), client); !errors.Is(err, syncer.ErrIncomplete) || run.BatchesDone() != 2 {
		t.Fatalf("outage run=%+v err=%v", run, err)
	}

	reopened, _ := backend.OpenSession(context.Background(), id)
	run, more, err := (&syncer.Engine{Store: reopened}).ContinueStep(context.Background(), feed(t, "happy.json"))
	if err != nil || more || run.Status != domain.RunCompleted || run.BatchesDone() != 3 || len(reopened.State().Products) != 4 {
		t.Fatalf("resume run=%+v more=%v products=%d err=%v", run, more, len(reopened.State().Products), err)
	}
}

func TestPostgresAssessmentScenarios(t *testing.T) {
	backend := database(t)
	t.Run("repeat", func(t *testing.T) {
		st := session(t, backend)
		engine := &syncer.Engine{Store: st}
		if _, err := engine.Start(context.Background(), "happy", feed(t, "happy.json")); err != nil {
			t.Fatal(err)
		}
		run, err := engine.Start(context.Background(), "happy", feed(t, "happy.json"))
		if err != nil || len(st.State().Products) != 4 || run.Counters.Unchanged != 4 || run.Counters.StaleIgnored != 2 {
			t.Fatalf("repeat run=%+v products=%d err=%v", run, len(st.State().Products), err)
		}
	})
	t.Run("out_of_order_withdrawal", func(t *testing.T) {
		st := session(t, backend)
		_, err := (&syncer.Engine{Store: st}).Start(context.Background(), "order", feed(t, "dup_out_of_order.json"))
		product := st.State().Products["B1"]
		if err != nil || product.Status != domain.StatusWithdrawn || product.Revision != 3 {
			t.Fatalf("product=%+v err=%v", product, err)
		}
	})
	t.Run("invalid_and_conflict", func(t *testing.T) {
		st := session(t, backend)
		run, err := (&syncer.Engine{Store: st}).Start(context.Background(), "bad", feed(t, "bad_records.json"))
		if err != nil || run.Status != domain.RunCompletedWithIssues || len(st.State().Issues) != 4 {
			t.Fatalf("run=%+v issues=%d err=%v", run, len(st.State().Issues), err)
		}
	})
	t.Run("missing_stays_incomplete", func(t *testing.T) {
		st := session(t, backend)
		run, err := (&syncer.Engine{Store: st}).Start(context.Background(), "missing", feed(t, "missing_batch.json"))
		if !errors.Is(err, syncer.ErrIncomplete) || run.BatchesDone() != 2 || run.Status != domain.RunInterrupted {
			t.Fatalf("run=%+v err=%v", run, err)
		}
	})
}

func TestPostgresIsolationAndConcurrentStart(t *testing.T) {
	backend := database(t)
	id := fmt.Sprintf("concurrent-%d", time.Now().UnixNano())
	a, _ := backend.OpenSession(context.Background(), id)
	b, _ := backend.OpenSession(context.Background(), id)
	clientA := feed(t, "happy.json")
	clientB := feed(t, "happy.json")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for index, repository := range []syncer.Repository{a, b} {
		client := []*supplier.FileClient{clientA, clientB}[index]
		wg.Add(1)
		go func(st syncer.Repository, supplierClient supplier.Client) {
			defer wg.Done()
			_, _, err := (&syncer.Engine{Store: st}).StartStep(context.Background(), "happy", supplierClient)
			errs <- err
		}(repository, client)
	}
	wg.Wait()
	close(errs)
	counts := map[bool]int{}
	for err := range errs {
		counts[errors.Is(err, store.ErrConcurrentUpdate)]++
	}
	if counts[false] != 1 || counts[true] != 1 {
		t.Fatalf("concurrent results = %+v", counts)
	}
	other := session(t, backend)
	if len(other.State().Runs) != 0 || len(other.State().Products) != 0 {
		t.Fatalf("new session is not isolated: %+v", other.State())
	}
}

func TestPostgresConcurrentResumeCommitsBatchOnce(t *testing.T) {
	backend := database(t)
	id := fmt.Sprintf("concurrent-resume-%d", time.Now().UnixNano())
	initial, _ := backend.OpenSession(context.Background(), id)
	client := feed(t, "happy.json")
	client.FailAfter = 2
	engine := &syncer.Engine{Store: initial}
	_, _, _ = engine.StartStep(context.Background(), "happy", client)
	_, _, _ = engine.ContinueStep(context.Background(), client)
	_, _, _ = engine.ContinueStep(context.Background(), client)

	a, _ := backend.OpenSession(context.Background(), id)
	b, _ := backend.OpenSession(context.Background(), id)
	clients := []*supplier.FileClient{feed(t, "happy.json"), feed(t, "happy.json")}
	repositories := []syncer.Repository{a, b}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index := range repositories {
		wg.Add(1)
		go func(st syncer.Repository, supplierClient supplier.Client) {
			defer wg.Done()
			_, _, err := (&syncer.Engine{Store: st}).ContinueStep(context.Background(), supplierClient)
			results <- err
		}(repositories[index], clients[index])
	}
	wg.Wait()
	close(results)
	counts := map[bool]int{}
	for err := range results {
		counts[errors.Is(err, store.ErrConcurrentUpdate)]++
	}
	if counts[false] != 1 || counts[true] != 1 {
		t.Fatalf("concurrent resume results = %+v", counts)
	}
	final, _ := backend.OpenSession(context.Background(), id)
	if final.State().OpenRun() != nil || len(final.State().Products) != 4 || final.State().LatestRun().Status != domain.RunCompleted {
		t.Fatalf("final state = %+v", final.State())
	}
}

func TestPostgresCancelledCommitIsAtomic(t *testing.T) {
	backend := database(t)
	id := fmt.Sprintf("cancel-%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	st, err := backend.OpenSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	next := st.State().Clone()
	next.Products["X"] = domain.Product{SKU: "X", Status: domain.StatusActive, Revision: 1}
	cancel()
	if err := st.Commit(next); err == nil {
		t.Fatal("cancelled commit succeeded")
	}
	reopened, _ := backend.OpenSession(context.Background(), id)
	if len(reopened.State().Products) != 0 {
		t.Fatal("failed transaction leaked product state")
	}
}
