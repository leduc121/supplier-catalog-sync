package webapp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type testClient struct {
	client *http.Client
	base   string
	csrf   string
	mu     sync.Mutex
}

func newTestClient(t *testing.T, handler http.Handler) (*testClient, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &testClient{client: &http.Client{Jar: jar}, base: server.URL}
	client.getState(t)
	return client, server.Close
}

func (c *testClient) getState(t *testing.T) stateResponse {
	t.Helper()
	response, err := c.client.Get(c.base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("state status = %d", response.StatusCode)
	}
	var state stateResponse
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.csrf = state.CSRFToken
	c.mu.Unlock()
	return state
}

func (c *testClient) post(t *testing.T, path string, body any) (int, mutationResponse) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	c.mu.Lock()
	csrf := c.csrf
	c.mu.Unlock()
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := c.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result mutationResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.State.CSRFToken != "" {
		c.mu.Lock()
		c.csrf = result.State.CSRFToken
		c.mu.Unlock()
	}
	return response.StatusCode, result
}

type mutationResponse struct {
	OK         bool          `json:"ok"`
	Incomplete bool          `json:"incomplete"`
	Code       string        `json:"code"`
	Error      string        `json:"error"`
	State      stateResponse `json:"state"`
	More       bool          `json:"more"`
}

func (c *testClient) run(t *testing.T, path string, body any) (int, mutationResponse) {
	t.Helper()
	for {
		status, result := c.post(t, path, body)
		if status != http.StatusOK || !result.More {
			return status, result
		}
		path, body = "/api/continue", struct{}{}
	}
}

func TestHTTPOutageResumeWorkflow(t *testing.T) {
	app, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client, closeServer := newTestClient(t, app.Handler())
	defer closeServer()

	status, result := client.run(t, "/api/start", startRequest{Scenario: "happy", FailAfter: 2})
	if status != http.StatusConflict || !result.Incomplete || result.State.OpenRun == nil {
		t.Fatalf("start status=%d result=%+v", status, result)
	}
	if result.State.OpenRun.BatchesDone() != 2 || len(result.State.Products) != 3 {
		t.Fatalf("incomplete state = %+v", result.State)
	}
	status, result = client.run(t, "/api/resume", struct{}{})
	if status != http.StatusOK || !result.OK || result.State.OpenRun != nil {
		t.Fatalf("resume status=%d result=%+v", status, result)
	}
	if result.State.LatestRun.Status != "completed" || len(result.State.Products) != 4 {
		t.Fatalf("completed state = %+v", result.State)
	}
}

func TestHTTPSessionsAreIsolated(t *testing.T) {
	app, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	clientA := clientForServer(t, server.URL)
	clientB := clientForServer(t, server.URL)
	clientA.getState(t)
	clientB.getState(t)
	if status, _ := clientA.run(t, "/api/start", startRequest{Scenario: "happy", FailAfter: -1}); status != http.StatusOK {
		t.Fatalf("start status = %d", status)
	}
	if got := clientB.getState(t); len(got.Products) != 0 || len(got.Runs) != 0 {
		t.Fatalf("second session saw first session data: %+v", got)
	}
}

func TestHTTPSessionSurvivesAppRestart(t *testing.T) {
	dataDir := t.TempDir()
	app, _ := New(dataDir)
	server := httptest.NewServer(app.Handler())
	client := clientForServer(t, server.URL)
	client.getState(t)
	if status, _ := client.run(t, "/api/start", startRequest{Scenario: "happy", FailAfter: -1}); status != http.StatusOK {
		t.Fatalf("start status = %d", status)
	}
	server.Close()

	restarted, _ := New(dataDir)
	server = httptest.NewServer(restarted.Handler())
	defer server.Close()
	client.base = server.URL
	state := client.getState(t)
	if len(state.Products) != 4 || state.LatestRun == nil || state.LatestRun.Status != "completed" {
		t.Fatalf("state after restart = %+v", state)
	}
}

func TestHTTPConcurrentStartsAreSerialized(t *testing.T) {
	app, _ := New(t.TempDir())
	client, closeServer := newTestClient(t, app.Handler())
	defer closeServer()
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := client.post(t, "/api/start", startRequest{Scenario: "missing_batch", FailAfter: -1})
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent statuses = %+v", counts)
	}
	state := client.getState(t)
	if len(state.Runs) != 1 || state.OpenRun == nil || state.OpenRun.BatchesDone() != 1 {
		t.Fatalf("serialized state = %+v", state)
	}
}

func TestHTTPRejectsUnsafeMutation(t *testing.T) {
	app, _ := New(t.TempDir())
	client, closeServer := newTestClient(t, app.Handler())
	defer closeServer()
	request, _ := http.NewRequest(http.MethodPost, client.base+"/api/start", bytes.NewBufferString(`{"scenario":"happy","fail_after":-1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	client.mu.Lock()
	csrf := client.csrf
	client.mu.Unlock()
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status = %d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodPost, client.base+"/api/start", bytes.NewBufferString(`{"scenario":"happy","fail_after":-1}`))
	request.Header.Set("Content-Type", "application/json")
	response, err = client.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("csrf status = %d", response.StatusCode)
	}
}

func TestHTTPSPublicOriginSetsSecureCookiesAndValidatesOrigin(t *testing.T) {
	app, err := New(t.TempDir(), "https://catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://catalog:8080/api/state", nil)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies = %d, want 2", len(cookies))
	}
	for _, cookie := range cookies {
		if !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("cookie %s is not secure and SameSite=Lax: %+v", cookie.Name, cookie)
		}
	}

	allowed := httptest.NewRequest(http.MethodPost, "http://catalog:8080/api/start", nil)
	allowed.Header.Set("Origin", "https://catalog.example.com")
	if !app.validOrigin(allowed) {
		t.Fatal("configured public origin was rejected")
	}
	allowed.Header.Set("Origin", "https://attacker.example")
	if app.validOrigin(allowed) {
		t.Fatal("foreign origin was allowed")
	}
}

func TestRejectsInvalidPublicOrigin(t *testing.T) {
	_, err := New(t.TempDir(), "https://catalog.example.com/path")
	if err == nil || !strings.Contains(err.Error(), "public origin") {
		t.Fatalf("error = %v", err)
	}
}

func clientForServer(t *testing.T, base string) *testClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &testClient{client: &http.Client{Jar: jar}, base: base}
}
