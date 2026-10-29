package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"supplier-catalog-sync/internal/domain"
)

var ErrOutage = errors.New("simulated supplier outage")

type Client interface {
	TotalBatches(context.Context) (int, error)
	FetchBatch(context.Context, int) (domain.Batch, error)
}
type FileClient struct {
	Total     int
	Batches   []domain.Batch
	FailAfter int
}
type feed struct {
	TotalBatches int            `json:"total_batches"`
	Batches      []domain.Batch `json:"batches"`
}

func OpenFile(path string) (*FileClient, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return OpenJSON(b)
}

// OpenJSON builds the fictional supplier client from embedded or uploaded-free data.
// Keeping parsing here means the CLI and web server use exactly the same feed rules.
func OpenJSON(b []byte) (*FileClient, error) {
	var f feed
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("read feed: %w", err)
	}
	if f.TotalBatches == 0 {
		f.TotalBatches = len(f.Batches)
	}
	return &FileClient{Total: f.TotalBatches, Batches: f.Batches, FailAfter: -1}, nil
}
func (c *FileClient) TotalBatches(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return c.Total, nil
}
func (c *FileClient) FetchBatch(ctx context.Context, seq int) (domain.Batch, error) {
	if err := ctx.Err(); err != nil {
		return domain.Batch{}, err
	}
	if c.FailAfter >= 0 && seq > c.FailAfter {
		return domain.Batch{}, ErrOutage
	}
	for _, b := range c.Batches {
		if b.Seq == seq {
			return b, nil
		}
	}
	return domain.Batch{}, fmt.Errorf("batch %d not delivered", seq)
}
