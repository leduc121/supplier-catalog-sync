package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"supplier-catalog-sync/internal/postgres"
	"supplier-catalog-sync/internal/webapp"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	port := env("PORT", "8080")
	dataDir := env("DATA_DIR", "data")
	publicOrigin := os.Getenv("PUBLIC_ORIGIN")
	var app *webapp.App
	var err error
	var closeDatabase func()
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		backend, openErr := postgres.Open(context.Background(), databaseURL)
		if openErr != nil {
			return openErr
		}
		closeDatabase = backend.Close
		app, err = webapp.NewWithBackend(backend, publicOrigin)
	} else {
		app, err = webapp.New(dataDir, publicOrigin)
	}
	if err != nil {
		return err
	}
	if closeDatabase != nil {
		defer closeDatabase()
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("Supplier Catalog listening on http://localhost:%s (data: %s)", port, dataDir)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve: %w", err)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
