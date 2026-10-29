package webapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/syncer"
)

type startRequest struct {
	Scenario  string `json:"scenario"`
	FailAfter int    `json:"fail_after"`
}

func (a *App) start(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !knownScenario(req.Scenario) {
		writeError(w, http.StatusBadRequest, "invalid_scenario", "Choose one of the available demo scenarios.")
		return
	}
	if req.FailAfter < -1 || req.FailAfter > scenarioBatches(req.Scenario) {
		writeError(w, http.StatusBadRequest, "invalid_fail_after", "Outage batch is outside this scenario.")
		return
	}
	a.mutate(w, r, func(ctx context.Context, st syncer.Repository) (domain.Run, bool, error) {
		client, err := scenarioClient(req.Scenario, req.FailAfter)
		if err != nil {
			return domain.Run{}, false, err
		}
		return (&syncer.Engine{Store: st, DemoFailAfter: &req.FailAfter}).StartStep(ctx, req.Scenario, client)
	})
}

func (a *App) resume(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	a.mutate(w, r, func(ctx context.Context, st syncer.Repository) (domain.Run, bool, error) {
		open := st.State().OpenRun()
		if open == nil {
			return domain.Run{}, false, errors.New("no update to resume")
		}
		client, err := scenarioClient(open.Source, -1)
		if err != nil {
			return domain.Run{}, false, err
		}
		return (&syncer.Engine{Store: st, ClearDemoFailure: true}).ContinueStep(ctx, client)
	})
}

func (a *App) continueRun(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	a.mutate(w, r, func(ctx context.Context, st syncer.Repository) (domain.Run, bool, error) {
		open := st.State().OpenRun()
		if open == nil {
			return domain.Run{}, false, errors.New("no update to resume")
		}
		failAfter := -1
		if open.DemoFailAfter != nil {
			failAfter = *open.DemoFailAfter
		}
		client, err := scenarioClient(open.Source, failAfter)
		if err != nil {
			return domain.Run{}, false, err
		}
		return (&syncer.Engine{Store: st}).ContinueStep(ctx, client)
	})
}

func (a *App) abort(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	a.mutate(w, r, func(_ context.Context, st syncer.Repository) (domain.Run, bool, error) {
		run, err := (&syncer.Engine{Store: st}).Abort()
		return run, false, err
	})
}

func (a *App) reset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !decodeJSON(w, r, &req) || !a.authorizeMutation(w, r) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, csrf, err := a.session(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	if r.Header.Get("X-CSRF-Token") != csrf {
		writeError(w, http.StatusForbidden, "csrf_rejected", "Security token is missing or invalid. Refresh and try again.")
		return
	}
	if req.Confirm != "RESET" {
		writeError(w, http.StatusBadRequest, "confirmation_required", "Type RESET to confirm.")
		return
	}
	if err := a.backend.ResetSession(r.Context(), session); err != nil {
		writeError(w, http.StatusInternalServerError, "reset_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"state": makeState(&store.State{Products: map[string]domain.Product{}}, csrf),
	})
}

func (a *App) mutate(w http.ResponseWriter, r *http.Request, action func(context.Context, syncer.Repository) (domain.Run, bool, error)) {
	if !a.authorizeMutation(w, r) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, csrf, err := a.session(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	if r.Header.Get("X-CSRF-Token") == "" || r.Header.Get("X-CSRF-Token") != csrf {
		writeError(w, http.StatusForbidden, "csrf_rejected", "Security token is missing or invalid. Refresh and try again.")
		return
	}
	st, err := a.backend.OpenSession(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalog_error", err.Error())
		return
	}
	run, more, actionErr := action(r.Context(), st)
	status, code, message := http.StatusOK, "", ""
	if actionErr != nil {
		status, code, message = http.StatusInternalServerError, "operation_failed", actionErr.Error()
		if errors.Is(actionErr, syncer.ErrIncomplete) {
			status, code = http.StatusConflict, "incomplete"
		} else if errors.Is(actionErr, syncer.ErrRunOpen) {
			status, code = http.StatusConflict, "run_open"
		} else if strings.Contains(actionErr.Error(), "no update") {
			status, code = http.StatusConflict, "no_open_run"
		} else if errors.Is(actionErr, store.ErrConcurrentUpdate) {
			status, code = http.StatusConflict, "concurrent_update"
		}
	}
	writeJSON(w, status, map[string]any{
		"ok": actionErr == nil, "incomplete": errors.Is(actionErr, syncer.ErrIncomplete),
		"code": code, "error": message, "run": run, "more": more, "state": makeState(st.State(), csrf),
	})
}

func (a *App) authorizeMutation(w http.ResponseWriter, r *http.Request) bool {
	if a.validOrigin(r) {
		return true
	}
	writeError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.")
	return false
}
