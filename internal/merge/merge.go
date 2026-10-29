package merge

import "supplier-catalog-sync/internal/domain"

type Decision string

const (
	Insert       Decision = "insert"
	Update       Decision = "update"
	Unchanged    Decision = "unchanged"
	StaleIgnored Decision = "stale_ignored"
	Conflict     Decision = "conflict"
)

// Decide uses only supplier revisions for ordering, so delivery order cannot corrupt newer data.
func Decide(cur *domain.Product, in domain.Record) Decision {
	if cur == nil {
		return Insert
	}
	if in.Revision < cur.Revision {
		return StaleIgnored
	}
	if in.Revision == cur.Revision {
		if cur.SameContent(in) {
			return Unchanged
		}
		return Conflict
	}
	return Update
}
