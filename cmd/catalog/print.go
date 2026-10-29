package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/store"
)

func printStatus(r domain.Run) {
	switch r.Status {
	case domain.RunCompleted:
		fmt.Printf("COMPLETED (%d/%d batches)\n", r.BatchesDone(), r.TotalBatches)
	case domain.RunCompletedWithIssues:
		fmt.Printf("COMPLETED WITH ISSUES (%d/%d batches)\n", r.BatchesDone(), r.TotalBatches)
	case domain.RunAborted:
		fmt.Println("ABORTED")
	default:
		fmt.Printf("INCOMPLETE - %s (%d/%d batches). Run `catalog resume`.\n", r.Status, r.BatchesDone(), r.TotalBatches)
	}
}
func printRunSummary(r domain.Run) { fmt.Printf("Run %d: ", r.ID); printStatus(r) }
func reportCommand(st *store.Store, args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	id := fs.Int("run", 0, "run ID")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == 0 {
		r := st.State().LatestRun()
		if r == nil {
			return fail(fmt.Errorf("no runs"))
		}
		*id = r.ID
	}
	if st.State().RunByID(*id) == nil {
		return fail(fmt.Errorf("run %d not found", *id))
	}
	printReport(st, *id)
	return 0
}
func printReport(st *store.Store, id int) {
	r := st.State().RunByID(id)
	if r == nil {
		return
	}
	fmt.Printf("Run %d: ", r.ID)
	printStatus(*r)
	c := r.Counters
	fmt.Printf("Source: %s\nCreated: %d  Updated: %d  Withdrawn: %d  Relisted: %d\nUnchanged: %d  Stale ignored: %d  Issues: %d\n", r.Source, c.Created, c.Updated, c.Withdrawn, c.Relisted, c.Unchanged, c.StaleIgnored, c.IssueCount)
	fmt.Println("Changes:")
	found := false
	for _, ch := range st.State().Changes {
		if ch.RunID != id {
			continue
		}
		found = true
		fmt.Printf("  %s: %s", ch.SKU, ch.Kind)
		if ch.Before != nil {
			fmt.Printf(" (%s -> %s)", productText(*ch.Before), productText(ch.After))
		} else {
			fmt.Printf(" (%s)", productText(ch.After))
		}
		fmt.Println()
	}
	if !found {
		fmt.Println("  none")
	}
	fmt.Println("Needs attention:")
	found = false
	for _, is := range st.State().Issues {
		if is.RunID != id {
			continue
		}
		found = true
		fmt.Printf("  batch %d, sku %q, %s: %s\n    raw: %s\n", is.BatchSeq, is.SKU, is.Kind, is.Detail, string(is.Raw))
	}
	if !found {
		fmt.Println("  none")
	}
	if r.LastError != "" {
		fmt.Println("Last error:", r.LastError)
	}
}
func productText(p domain.Product) string {
	return fmt.Sprintf("%s, %s, %s, r%d", p.Name, money(p.PriceCents), p.Status, p.Revision)
}
func money(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
func productsCommand(st *store.Store, args []string) int {
	fs := flag.NewFlagSet("products", flag.ContinueOnError)
	status := fs.String("status", "", "active or withdrawn")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *status != "" && *status != domain.StatusActive && *status != domain.StatusWithdrawn {
		return fail(fmt.Errorf("status must be active or withdrawn"))
	}
	keys := make([]string, 0, len(st.State().Products))
	for sku, p := range st.State().Products {
		if *status == "" || p.Status == *status {
			keys = append(keys, sku)
		}
	}
	sort.Strings(keys)
	for _, sku := range keys {
		p := st.State().Products[sku]
		fmt.Printf("%s  %-10s  %-20s  %8s  r%d\n", p.SKU, strings.ToUpper(p.Status), p.Name, money(p.PriceCents), p.Revision)
	}
	return 0
}
