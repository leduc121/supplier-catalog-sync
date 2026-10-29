package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/merge"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/supplier"
	"time"
)

var ErrIncomplete = errors.New("catalog update incomplete")
var ErrRunOpen = errors.New("an update is already open; resume or abort it")

type Engine struct {
	Store      *store.Store
	Now        func() time.Time
	AfterBatch func(int)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) Start(ctx context.Context, source string, client supplier.Client) (domain.Run, error) {
	if e.Store.State().OpenRun() != nil {
		return domain.Run{}, ErrRunOpen
	}
	next := e.Store.State().Clone()
	r := domain.Run{ID: next.NextRunID(), Source: source, Status: domain.RunRunning, NextSeq: 1, StartedAt: e.now()}
	next.Runs = append(next.Runs, r)
	if err := e.Store.Commit(next); err != nil {
		return r, err
	}
	return e.process(ctx, client)
}
func (e *Engine) Resume(ctx context.Context, client supplier.Client) (domain.Run, error) {
	if e.Store.State().OpenRun() == nil {
		return domain.Run{}, errors.New("no update to resume")
	}
	return e.process(ctx, client)
}
func (e *Engine) Abort() (domain.Run, error) {
	cur := e.Store.State().OpenRun()
	if cur == nil {
		return domain.Run{}, errors.New("no update to abort")
	}
	next := e.Store.State().Clone()
	r := next.RunByID(cur.ID)
	r.Status = domain.RunAborted
	r.LastError = ""
	now := e.now()
	r.FinishedAt = &now
	if err := e.Store.Commit(next); err != nil {
		return *r, err
	}
	return *r, nil
}

func (e *Engine) process(ctx context.Context, client supplier.Client) (domain.Run, error) {
	open := e.Store.State().OpenRun()
	if open == nil {
		return domain.Run{}, errors.New("no open update")
	}
	runID := open.ID
	total, err := client.TotalBatches(ctx)
	if err != nil {
		return e.interrupt(runID, err)
	}
	next := e.Store.State().Clone()
	r := next.RunByID(runID)
	if r.TotalBatches != 0 && r.TotalBatches != total {
		return e.interrupt(runID, fmt.Errorf("feed total changed from %d to %d", r.TotalBatches, total))
	}
	r.TotalBatches = total
	r.Status = domain.RunRunning
	r.LastError = ""
	if err = e.Store.Commit(next); err != nil {
		return *r, err
	}
	for {
		r = e.Store.State().RunByID(runID)
		if r.NextSeq > total {
			break
		}
		if err = ctx.Err(); err != nil {
			return e.interrupt(runID, err)
		}
		seq := r.NextSeq
		var batch domain.Batch
		batch, err = client.FetchBatch(ctx, seq)
		if err != nil {
			return e.interrupt(runID, err)
		}
		if batch.Seq != seq {
			return e.interrupt(runID, fmt.Errorf("expected batch %d, got %d", seq, batch.Seq))
		}
		next = e.Store.State().Clone()
		applyBatch(next, next.RunByID(runID), batch)
		next.RunByID(runID).NextSeq++
		// One commit includes catalog changes, audit data, counters and cursor, so resume never repeats partial work.
		if err = e.Store.Commit(next); err != nil {
			return *next.RunByID(runID), err
		}
		if e.AfterBatch != nil {
			e.AfterBatch(seq)
		}
	}
	r = e.Store.State().RunByID(runID)
	if r.BatchesDone() != total {
		return e.interrupt(runID, errors.New("not all batches were processed"))
	}
	next = e.Store.State().Clone()
	r = next.RunByID(runID)
	r.Status = domain.RunCompleted
	if r.Counters.IssueCount > 0 {
		r.Status = domain.RunCompletedWithIssues
	}
	now := e.now()
	r.FinishedAt = &now
	if err = e.Store.Commit(next); err != nil {
		return *r, err
	}
	return *r, nil
}

func (e *Engine) interrupt(id int, cause error) (domain.Run, error) {
	next := e.Store.State().Clone()
	r := next.RunByID(id)
	r.Status = domain.RunInterrupted
	r.LastError = cause.Error()
	if err := e.Store.Commit(next); err != nil {
		return *r, err
	}
	return *r, fmt.Errorf("%w: %v", ErrIncomplete, cause)
}

func applyBatch(s *store.State, r *domain.Run, b domain.Batch) {
	for _, in := range b.Records {
		applyRecord(s, r, b.Seq, in)
	}
}
func applyRecord(s *store.State, r *domain.Run, seq int, in domain.Record) {
	if err := in.Validate(); err != nil {
		addIssue(s, r, seq, in, "invalid_record", err.Error())
		return
	}
	curValue, exists := s.Products[in.SKU]
	var cur *domain.Product
	if exists {
		cur = &curValue
	}
	switch merge.Decide(cur, in) {
	case merge.Insert:
		after := domain.Product{}.FromRecord(in, r.ID)
		s.Products[in.SKU] = after
		r.Counters.Created++
		kind := "created"
		if in.Status == domain.StatusWithdrawn {
			r.Counters.Withdrawn++
			kind = "withdrawn"
		}
		s.Changes = append(s.Changes, domain.Change{RunID: r.ID, SKU: in.SKU, Kind: kind, After: after})
	case merge.Update:
		after := curValue.FromRecord(in, r.ID)
		s.Products[in.SKU] = after
		if curValue.Status == domain.StatusActive && in.Status == domain.StatusWithdrawn {
			r.Counters.Withdrawn++
			addChange(s, r, "withdrawn", curValue, after)
		} else if curValue.Status == domain.StatusWithdrawn && in.Status == domain.StatusActive {
			r.Counters.Relisted++
			addChange(s, r, "relisted", curValue, after)
		} else if curValue.Status == domain.StatusWithdrawn {
			r.Counters.Unchanged++
		} else {
			r.Counters.Updated++
			addChange(s, r, "updated", curValue, after)
		}
	case merge.Unchanged:
		r.Counters.Unchanged++
	case merge.StaleIgnored:
		r.Counters.StaleIgnored++
	case merge.Conflict:
		addIssue(s, r, seq, in, "revision_conflict", "same revision has different content")
	}
}
func addChange(s *store.State, r *domain.Run, kind string, before, after domain.Product) {
	b := before
	s.Changes = append(s.Changes, domain.Change{RunID: r.ID, SKU: after.SKU, Kind: kind, Before: &b, After: after})
}
func addIssue(s *store.State, r *domain.Run, seq int, in domain.Record, kind, detail string) {
	raw, _ := json.Marshal(in)
	s.Issues = append(s.Issues, domain.Issue{RunID: r.ID, BatchSeq: seq, SKU: in.SKU, Kind: kind, Detail: detail, Raw: raw})
	r.Counters.IssueCount++
}
