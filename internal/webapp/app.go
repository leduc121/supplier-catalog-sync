package webapp

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/syncer"
	"sync"
)

const sessionCookie = "catalog_session"
const csrfCookie = "catalog_csrf"

//go:embed static/* feeds/*
var assets embed.FS

// App keeps local JSON access serialized. PostgreSQL additionally enforces
// cross-instance optimistic concurrency at every durable commit.
type App struct {
	backend      Backend
	mu           sync.Mutex
	static       http.Handler
	publicOrigin string
}

type Backend interface {
	OpenSession(context.Context, string) (syncer.Repository, error)
	ResetSession(context.Context, string) error
}

type fileBackend struct {
	dataDir string
}

func (b *fileBackend) OpenSession(_ context.Context, session string) (syncer.Repository, error) {
	return store.Open(filepath.Join(b.dataDir, "sessions", session, "catalog.json"))
}

func (b *fileBackend) ResetSession(_ context.Context, session string) error {
	err := os.Remove(filepath.Join(b.dataDir, "sessions", session, "catalog.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func New(dataDir string, origins ...string) (*App, error) {
	if dataDir == "" {
		return nil, errors.New("data directory must not be empty")
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "sessions"), 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	publicOrigin := ""
	if len(origins) > 0 {
		publicOrigin = origins[0]
	}
	var err error
	publicOrigin, err = validatePublicOrigin(publicOrigin)
	if err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	return newWithBackend(&fileBackend{dataDir: dataDir}, publicOrigin, staticFS), nil
}

func NewWithBackend(backend Backend, publicOrigin string) (*App, error) {
	if backend == nil {
		return nil, errors.New("backend is required")
	}
	parsedOrigin, err := validatePublicOrigin(publicOrigin)
	if err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	return newWithBackend(backend, parsedOrigin, staticFS), nil
}

func newWithBackend(backend Backend, publicOrigin string, staticFS fs.FS) *App {
	return &App{backend: backend, static: http.FileServer(http.FS(staticFS)), publicOrigin: publicOrigin}
}

func validatePublicOrigin(publicOrigin string) (string, error) {
	publicOrigin = strings.TrimRight(publicOrigin, "/")
	if publicOrigin == "" {
		return "", nil
	}
	parsed, err := url.Parse(publicOrigin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("public origin must be an http(s) origin without a path, query, or fragment")
	}
	return publicOrigin, nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", onlyMethod(http.MethodGet, http.HandlerFunc(a.health)))
	mux.Handle("/api/state", onlyMethod(http.MethodGet, http.HandlerFunc(a.state)))
	mux.Handle("/api/report", onlyMethod(http.MethodGet, http.HandlerFunc(a.report)))
	mux.Handle("/api/start", onlyMethod(http.MethodPost, http.HandlerFunc(a.start)))
	mux.Handle("/api/resume", onlyMethod(http.MethodPost, http.HandlerFunc(a.resume)))
	mux.Handle("/api/continue", onlyMethod(http.MethodPost, http.HandlerFunc(a.continueRun)))
	mux.Handle("/api/abort", onlyMethod(http.MethodPost, http.HandlerFunc(a.abort)))
	mux.Handle("/api/reset", onlyMethod(http.MethodPost, http.HandlerFunc(a.reset)))
	mux.HandleFunc("/", a.frontend)
	return securityHeaders(mux)
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) frontend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and HEAD are allowed.")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found.")
		return
	}
	if r.URL.Path == "/" {
		index, err := assets.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "frontend unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
		return
	}
	a.static.ServeHTTP(w, r)
}

func (a *App) session(w http.ResponseWriter, r *http.Request) (string, string, error) {
	session := cookieValue(r, sessionCookie)
	if !validToken(session) {
		var err error
		session, err = randomToken()
		if err != nil {
			return "", "", err
		}
		a.setCookie(w, r, sessionCookie, session, true)
	}
	csrf := cookieValue(r, csrfCookie)
	if !validToken(csrf) {
		var err error
		csrf, err = randomToken()
		if err != nil {
			return "", "", err
		}
		a.setCookie(w, r, csrfCookie, csrf, false)
	}
	return session, csrf, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func validToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (a *App) setCookie(w http.ResponseWriter, r *http.Request, name, value string, httpOnly bool) {
	secure := r.TLS != nil || strings.HasPrefix(a.publicOrigin, "https://")
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: httpOnly, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 60 * 60 * 24 * 365})
}
