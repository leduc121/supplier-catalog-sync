package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"supplier-catalog-sync/internal/postgres"
	"testing"
)

func TestResolveOrigin(t *testing.T) {
	t.Run("public origin precedence", func(t *testing.T) {
		t.Setenv("PUBLIC_ORIGIN", "https://custom.example.com/")
		t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "proj.vercel.app")
		t.Setenv("VERCEL_URL", "branch.vercel.app")
		if got := resolveOrigin(); got != "https://custom.example.com" {
			t.Fatalf("got %q, want https://custom.example.com", got)
		}
	})

	t.Run("vercel project production url fallback", func(t *testing.T) {
		t.Setenv("PUBLIC_ORIGIN", "")
		t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "proj.vercel.app")
		t.Setenv("VERCEL_URL", "branch.vercel.app")
		if got := resolveOrigin(); got != "https://proj.vercel.app" {
			t.Fatalf("got %q, want https://proj.vercel.app", got)
		}
	})

	t.Run("vercel url fallback", func(t *testing.T) {
		t.Setenv("PUBLIC_ORIGIN", "")
		t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "")
		t.Setenv("VERCEL_URL", "deployment.vercel.app")
		if got := resolveOrigin(); got != "https://deployment.vercel.app" {
			t.Fatalf("got %q, want https://deployment.vercel.app", got)
		}
	})

	t.Run("empty fallback", func(t *testing.T) {
		t.Setenv("PUBLIC_ORIGIN", "")
		t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "")
		t.Setenv("VERCEL_URL", "")
		if got := resolveOrigin(); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestHandlerWithoutDatabaseURL(t *testing.T) {
	mu.Lock()
	application = nil
	mu.Unlock()

	t.Setenv("DATABASE_URL", "")
	req := httptest.NewRequest(http.MethodGet, "/api/index?__path=/healthz", nil)
	rec := httptest.NewRecorder()

	Handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "DATABASE_URL") {
		t.Fatalf("body = %q, expected DATABASE_URL error", rec.Body.String())
	}
}

func TestHandlerWithRealPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping live Vercel handler integration test")
	}

	if err := postgres.Migrate(context.Background(), url); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	application = nil
	mu.Unlock()

	t.Setenv("DATABASE_URL", url)
	t.Setenv("PUBLIC_ORIGIN", "https://catalog.example.com")

	t.Run("healthz endpoint via rewrite", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/index?__path=/healthz", nil)
		rec := httptest.NewRecorder()
		Handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("health status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
			t.Fatalf("unexpected health body: %s", rec.Body.String())
		}
	})

	t.Run("root static page via rewrite", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/index?__path=/", nil)
		rec := httptest.NewRecorder()
		Handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("root status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<title>Supplier Catalog</title>") {
			t.Fatalf("unexpected root body: %s", rec.Body.String())
		}
	})

	t.Run("api state endpoint via rewrite", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/index?__path=/api/state", nil)
		rec := httptest.NewRecorder()
		Handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("state status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"products"`) {
			t.Fatalf("unexpected state body: %s", rec.Body.String())
		}
	})
}
