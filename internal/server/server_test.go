package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kyan9400/webhook-workbench/internal/store"
)

func testServer(t *testing.T, token string, maxBody int64) (*Server, *store.Store) {
	t.Helper()
	events, err := store.New("", 20)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Store: events, Token: token, MaxBody: maxBody,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:         func() time.Time { return time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC) },
		IDGenerator: func() (string, error) { return "event-123", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, events
}

func TestCaptureRedactsSensitiveHeaders(t *testing.T) {
	server, events := testServer(t, "secret", 1024)
	unauthorized := httptest.NewRequest(http.MethodPost, "/inbox/orders", strings.NewReader(`{"id":1}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/inbox/orders?source=test", strings.NewReader(`{"id":1}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Api-Key", "private")
	request.Header.Set("X-Trace-Id", "trace-42")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", response.Code, response.Body.String())
	}

	got, err := events.Get("event-123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["Authorization"][0] != "[REDACTED]" {
		t.Fatalf("authorization not redacted: %#v", got.Headers)
	}
	if got.Headers["X-Api-Key"][0] != "[REDACTED]" {
		t.Fatalf("api key not redacted: %#v", got.Headers)
	}
	if got.Headers["X-Trace-Id"][0] != "trace-42" {
		t.Fatalf("ordinary header missing: %#v", got.Headers)
	}
	if got.Query != "source=test" || got.Body != `{"id":1}` || got.BodyEncoding != "utf-8" {
		t.Fatalf("unexpected event: %#v", got)
	}
}

func TestCaptureTruncatesAndEncodesBinary(t *testing.T) {
	server, events := testServer(t, "", 4)
	request := httptest.NewRequest(http.MethodPut, "/inbox/files", strings.NewReader("12345"))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	got, _ := events.Get("event-123")
	if !got.Truncated || got.Body != "1234" || got.Size != 4 {
		t.Fatalf("unexpected truncation: %#v", got)
	}

	server, events = testServer(t, "", 10)
	request = httptest.NewRequest(http.MethodPost, "/inbox/files", strings.NewReader(string([]byte{0xff, 0x00})))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	got, _ = events.Get("event-123")
	if got.BodyEncoding != "base64" || got.Body != base64.StdEncoding.EncodeToString([]byte{0xff, 0x00}) {
		t.Fatalf("unexpected binary encoding: %#v", got)
	}
}

func TestEventAPI(t *testing.T) {
	server, events := testServer(t, "", 1024)
	_ = events.Add(store.Event{ID: "one", Channel: "orders", Headers: map[string][]string{}, ReceivedAt: time.Now()})
	_ = events.Add(store.Event{ID: "two", Channel: "billing", Headers: map[string][]string{}, ReceivedAt: time.Now()})

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/events?channel=orders", nil))
	var listed struct {
		Events []store.Summary `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Events) != 1 || listed.Events[0].ID != "one" {
		t.Fatalf("unexpected response: %#v", listed)
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/events/one", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/events/one", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func TestReplayForwardsCapturedRequest(t *testing.T) {
	type receivedRequest struct {
		method        string
		body          string
		trace         string
		authorization string
	}
	received := make(chan receivedRequest, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read replay body: %v", err)
		}
		received <- receivedRequest{
			method:        r.Method,
			body:          string(body),
			trace:         r.Header.Get("X-Trace-Id"),
			authorization: r.Header.Get("Authorization"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer target.Close()

	events, err := store.New("", 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := events.Add(store.Event{
		ID: "replay-one", Method: http.MethodPatch, Body: `{"id":42}`, BodyEncoding: "utf-8",
		Headers: map[string][]string{
			"Content-Type":  {"application/json"},
			"X-Trace-Id":    {"trace-42"},
			"Authorization": {"[REDACTED]"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Store: events, MaxBody: 1024, AllowPrivateReplay: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	requestBody := strings.NewReader(`{"targetUrl":"` + target.URL + `/receiver"}`)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/events/replay-one/replay", requestBody))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	gotRequest := <-received
	if gotRequest.method != http.MethodPatch || gotRequest.body != `{"id":42}` || gotRequest.trace != "trace-42" {
		t.Fatalf("unexpected replay request: %#v", gotRequest)
	}
	if gotRequest.authorization != "" {
		t.Fatalf("sensitive header was replayed: %q", gotRequest.authorization)
	}
	var got replayResponse
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.StatusCode != http.StatusCreated || got.Body != `{"accepted":true}` || got.BodyEncoding != "utf-8" {
		t.Fatalf("unexpected replay response: %#v", got)
	}
}

func TestReplayBlocksUnsafeTargets(t *testing.T) {
	server, events := testServer(t, "", 1024)
	if err := events.Add(store.Event{ID: "blocked", Method: http.MethodPost, Headers: map[string][]string{}}); err != nil {
		t.Fatal(err)
	}

	tests := []string{
		`{"targetUrl":"http://127.0.0.1:9999/callback"}`,
		`{"targetUrl":"ftp://example.com/callback"}`,
		`{"targetUrl":"https://user:password@example.com/callback"}`,
	}
	for _, body := range tests {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/events/blocked/replay", strings.NewReader(body))
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", body, response.Code, response.Body.String())
		}
	}
}

func TestValidationHealthAndSecurityHeaders(t *testing.T) {
	server, _ := testServer(t, "", 1024)
	for _, path := range []string{"/inbox/", "/inbox/not/valid", "/inbox/$bad"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("%s: expected validation failure, got %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("unexpected health response: %#v", response)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"authRequired":false`) {
		t.Fatalf("unexpected config response: %d %s", response.Code, response.Body.String())
	}
}

func TestIndexAndMethodHandling(t *testing.T) {
	server, _ := testServer(t, "", 1024)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Webhook Workbench") {
		t.Fatal("index not served")
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/events", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") == "" {
		t.Fatalf("unexpected response: %d", response.Code)
	}
}
