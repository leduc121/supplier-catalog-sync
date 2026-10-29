package domain

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	StatusActive    = "active"
	StatusWithdrawn = "withdrawn"
)

type Record struct {
	SKU        string `json:"sku"`
	Name       string `json:"name,omitempty"`
	PriceCents int64  `json:"price_cents,omitempty"`
	Status     string `json:"status"`
	Revision   int    `json:"revision"`
}

func (r Record) Validate() error {
	if r.SKU == "" {
		return errors.New("sku must not be empty")
	}
	if r.Revision < 1 {
		return errors.New("revision must be at least 1")
	}
	if r.Status != StatusActive && r.Status != StatusWithdrawn {
		return errors.New("status must be active or withdrawn")
	}
	if r.Status == StatusActive && r.Name == "" {
		return errors.New("active record needs a name")
	}
	if r.Status == StatusActive && r.PriceCents < 0 {
		return errors.New("active record price must not be negative")
	}
	return nil
}

type Product struct {
	SKU          string `json:"sku"`
	Name         string `json:"name"`
	PriceCents   int64  `json:"price_cents"`
	Status       string `json:"status"`
	Revision     int    `json:"revision"`
	UpdatedInRun int    `json:"updated_in_run"`
}

func (p Product) SameContent(r Record) bool {
	if p.Status != r.Status {
		return false
	}
	if p.Status == StatusWithdrawn {
		return true
	}
	return p.Name == r.Name && p.PriceCents == r.PriceCents
}

func (p Product) FromRecord(r Record, runID int) Product {
	if r.Status == StatusWithdrawn {
		p.SKU, p.Status, p.Revision, p.UpdatedInRun = r.SKU, r.Status, r.Revision, runID
		return p
	}
	return Product{SKU: r.SKU, Name: r.Name, PriceCents: r.PriceCents, Status: r.Status, Revision: r.Revision, UpdatedInRun: runID}
}

type Batch struct {
	Seq     int      `json:"seq"`
	Records []Record `json:"records"`
}

const (
	RunRunning             = "running"
	RunInterrupted         = "interrupted"
	RunCompleted           = "completed"
	RunCompletedWithIssues = "completed_with_issues"
	RunAborted             = "aborted"
)

type Counters struct {
	Created      int `json:"created"`
	Updated      int `json:"updated"`
	Withdrawn    int `json:"withdrawn"`
	Relisted     int `json:"relisted"`
	Unchanged    int `json:"unchanged"`
	StaleIgnored int `json:"stale_ignored"`
	IssueCount   int `json:"issue_count"`
}

type Run struct {
	ID           int        `json:"id"`
	Source       string     `json:"source"`
	Status       string     `json:"status"`
	NextSeq      int        `json:"next_seq"`
	TotalBatches int        `json:"total_batches"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Counters     Counters   `json:"counters"`
}

func (r Run) BatchesDone() int { return r.NextSeq - 1 }
func (r Run) IsOpen() bool     { return r.Status == RunRunning || r.Status == RunInterrupted }

type Issue struct {
	RunID    int             `json:"run_id"`
	BatchSeq int             `json:"batch_seq"`
	SKU      string          `json:"sku"`
	Kind     string          `json:"kind"`
	Detail   string          `json:"detail"`
	Raw      json.RawMessage `json:"raw"`
}
type Change struct {
	RunID  int      `json:"run_id"`
	SKU    string   `json:"sku"`
	Kind   string   `json:"kind"`
	Before *Product `json:"before,omitempty"`
	After  Product  `json:"after"`
}
