package merge

import (
	"supplier-catalog-sync/internal/domain"
	"testing"
)

func TestDecide(t *testing.T) {
	a := domain.Product{SKU: "X", Name: "Old", PriceCents: 10, Status: domain.StatusActive, Revision: 2}
	w := domain.Product{SKU: "X", Name: "Old", PriceCents: 10, Status: domain.StatusWithdrawn, Revision: 3}
	tests := []struct {
		name string
		cur  *domain.Product
		in   domain.Record
		want Decision
	}{
		{"new product", nil, domain.Record{SKU: "X", Status: "active", Revision: 1}, Insert},
		{"withdrawn first", nil, domain.Record{SKU: "X", Status: "withdrawn", Revision: 3}, Insert},
		{"price change", &a, domain.Record{SKU: "X", Name: "Old", PriceCents: 20, Status: "active", Revision: 3}, Update},
		{"exact repeat", &a, domain.Record{SKU: "X", Name: "Old", PriceCents: 10, Status: "active", Revision: 2}, Unchanged},
		{"older info", &a, domain.Record{SKU: "X", Status: "active", Revision: 1}, StaleIgnored},
		{"withdraw", &a, domain.Record{SKU: "X", Status: "withdrawn", Revision: 3}, Update},
		{"old active cannot resurrect", &w, domain.Record{SKU: "X", Name: "New", PriceCents: 20, Status: "active", Revision: 2}, StaleIgnored},
		{"repeat withdrawn", &w, domain.Record{SKU: "X", Status: "withdrawn", Revision: 3}, Unchanged},
		{"relist newer", &w, domain.Record{SKU: "X", Name: "New", PriceCents: 20, Status: "active", Revision: 4}, Update},
		{"same revision content conflict", &a, domain.Record{SKU: "X", Name: "Changed", PriceCents: 10, Status: "active", Revision: 2}, Conflict},
		{"same revision status conflict", &a, domain.Record{SKU: "X", Status: "withdrawn", Revision: 2}, Conflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.cur, tt.in); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}
