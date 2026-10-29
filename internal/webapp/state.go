package webapp

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/supplier"
)

type Scenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Batches     int    `json:"batches"`
}

var scenarios = []Scenario{
	{ID: "happy", Name: "Normal catalog", Description: "New products, a price update and a withdrawal.", Batches: 3},
	{ID: "dup_out_of_order", Name: "Repeated and out of order", Description: "Duplicates and stale records arrive after newer revisions.", Batches: 3},
	{ID: "bad_records", Name: "Invalid records", Description: "Valid rows continue while four issues need attention.", Batches: 2},
	{ID: "missing_batch", Name: "Missing final batch", Description: "The supplier declares three batches but delivers only two.", Batches: 3},
}

type stateResponse struct {
	Products  []domain.Product `json:"products"`
	Runs      []domain.Run     `json:"runs"`
	Issues    []domain.Issue   `json:"issues"`
	Changes   []domain.Change  `json:"changes"`
	Scenarios []Scenario       `json:"scenarios"`
	OpenRun   *domain.Run      `json:"open_run,omitempty"`
	LatestRun *domain.Run      `json:"latest_run,omitempty"`
	CSRFToken string           `json:"csrf_token"`
}

func (a *App) state(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session, csrf, err := a.session(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	st, err := a.backend.OpenSession(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalog_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makeState(st.State(), csrf))
}

func (a *App) report(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session, csrf, err := a.session(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	id, err := strconv.Atoi(r.URL.Query().Get("run"))
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_run", "A positive run ID is required.")
		return
	}
	st, err := a.backend.OpenSession(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalog_error", err.Error())
		return
	}
	if st.State().RunByID(id) == nil {
		writeError(w, http.StatusNotFound, "run_not_found", "Run not found.")
		return
	}
	state := makeState(st.State(), csrf)
	state.Runs = filterRuns(state.Runs, id)
	state.Issues = filterIssues(state.Issues, id)
	state.Changes = filterChanges(state.Changes, id)
	writeJSON(w, http.StatusOK, state)
}

func makeState(s *store.State, csrf string) stateResponse {
	products := make([]domain.Product, 0, len(s.Products))
	for _, product := range s.Products {
		products = append(products, product)
	}
	sort.Slice(products, func(i, j int) bool { return products[i].SKU < products[j].SKU })
	runs := append([]domain.Run{}, s.Runs...)
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	response := stateResponse{
		Products:  products,
		Runs:      runs,
		Issues:    append([]domain.Issue{}, s.Issues...),
		Changes:   append([]domain.Change{}, s.Changes...),
		Scenarios: scenarios,
		CSRFToken: csrf,
	}
	if open := s.OpenRun(); open != nil {
		copy := *open
		response.OpenRun = &copy
	}
	if latest := s.LatestRun(); latest != nil {
		copy := *latest
		response.LatestRun = &copy
	}
	return response
}

func scenarioClient(id string, failAfter int) (*supplier.FileClient, error) {
	b, err := assets.ReadFile("feeds/" + id + ".json")
	if err != nil {
		return nil, errors.New("saved scenario is unavailable")
	}
	client, err := supplier.OpenJSON(b)
	if err != nil {
		return nil, err
	}
	client.FailAfter = failAfter
	return client, nil
}

func knownScenario(id string) bool {
	for _, scenario := range scenarios {
		if scenario.ID == id {
			return true
		}
	}
	return false
}

func scenarioBatches(id string) int {
	for _, scenario := range scenarios {
		if scenario.ID == id {
			return scenario.Batches
		}
	}
	return 0
}

func filterRuns(items []domain.Run, id int) []domain.Run {
	for _, item := range items {
		if item.ID == id {
			return []domain.Run{item}
		}
	}
	return nil
}

func filterIssues(items []domain.Issue, id int) []domain.Issue {
	result := []domain.Issue{}
	for _, item := range items {
		if item.RunID == id {
			result = append(result, item)
		}
	}
	return result
}

func filterChanges(items []domain.Change, id int) []domain.Change {
	result := []domain.Change{}
	for _, item := range items {
		if item.RunID == id {
			result = append(result, item)
		}
	}
	return result
}
