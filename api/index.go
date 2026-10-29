package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"supplier-catalog-sync/internal/postgres"
	"supplier-catalog-sync/internal/webapp"
	"sync"
	"time"
)

var (
	mu          sync.RWMutex
	application http.Handler
)

func resolveOrigin() string {
	origin := strings.TrimRight(os.Getenv("PUBLIC_ORIGIN"), "/")
	if origin != "" {
		return origin
	}
	if prodURL := os.Getenv("VERCEL_PROJECT_PRODUCTION_URL"); prodURL != "" {
		return "https://" + prodURL
	}
	if vercelURL := os.Getenv("VERCEL_URL"); vercelURL != "" {
		return "https://" + vercelURL
	}
	return ""
}

func getApplication() (http.Handler, error) {
	mu.RLock()
	if application != nil {
		app := application
		mu.RUnlock()
		return app, nil
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if application != nil {
		return application, nil
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL environment variable is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	backend, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		log.Printf("[api/index] failed to connect to postgres: %v", err)
		return nil, err
	}

	origin := resolveOrigin()
	app, err := webapp.NewWithBackend(backend, origin)
	if err != nil {
		backend.Close()
		log.Printf("[api/index] failed to initialize webapp: %v", err)
		return nil, err
	}

	application = app.Handler()
	return application, nil
}

func Handler(w http.ResponseWriter, r *http.Request) {
	app, err := getApplication()
	if err != nil {
		http.Error(w, "application configuration is unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	if path := r.URL.Query().Get("__path"); path != "" {
		query := r.URL.Query()
		query.Del("__path")
		r.URL.RawQuery = query.Encode()
		r.URL.Path = path
	}

	app.ServeHTTP(w, r)
}
