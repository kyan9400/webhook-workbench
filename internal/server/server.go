package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kyan9400/webhook-workbench/internal/store"
	"github.com/kyan9400/webhook-workbench/internal/webui"
)

var channelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var sensitiveHeaders = map[string]struct{}{
	"authorization":       {},
	"cookie":              {},
	"proxy-authorization": {},
	"set-cookie":          {},
	"x-api-key":           {},
	"x-auth-token":        {},
}

type Config struct {
	Store       *store.Store
	Token       string
	MaxBody     int64
	Logger      *slog.Logger
	Now         func() time.Time
	IDGenerator func() (string, error)
}

type Server struct {
	store       *store.Store
	token       string
	maxBody     int64
	logger      *slog.Logger
	now         func() time.Time
	idGenerator func() (string, error)
}

func New(config Config) (*Server, error) {
	if config.Store == nil {
		return nil, errors.New("store is required")
	}
	if config.MaxBody < 1 {
		return nil, errors.New("max body must be at least 1 byte")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.IDGenerator == nil {
		config.IDGenerator = randomID
	}
	return &Server{
		store: config.Store, token: config.Token, maxBody: config.MaxBody,
		logger: config.Logger, now: config.Now, idGenerator: config.IDGenerator,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveIndex)
	mux.HandleFunc("/healthz", s.serveHealth)
	mux.HandleFunc("/api/config", s.serveConfig)
	mux.HandleFunc("/inbox/", s.withAuth(s.capture))
	mux.HandleFunc("/api/events", s.withAuth(s.events))
	mux.HandleFunc("/api/events/", s.withAuth(s.event))
	return s.securityHeaders(s.recoverPanic(s.logRequests(mux)))
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_, _ = w.Write(webui.Index)
	}
}

func (s *Server) serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) serveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authRequired": s.token != "", "maxBody": s.maxBody})
}

func (s *Server) capture(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimPrefix(r.URL.Path, "/inbox/")
	if !channelPattern.MatchString(channel) {
		writeError(w, http.StatusBadRequest, "channel must contain 1-64 letters, numbers, dashes, or underscores")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	truncated := int64(len(body)) > s.maxBody
	if truncated {
		body = body[:s.maxBody]
	}
	id, err := s.idGenerator()
	if err != nil {
		s.logger.Error("generate event id", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create event")
		return
	}
	encodedBody, encoding := encodeBody(body)
	event := store.Event{
		ID: id, Channel: channel, Method: r.Method, Path: r.URL.Path,
		Query: r.URL.RawQuery, RemoteAddr: remoteHost(r.RemoteAddr), ReceivedAt: s.now().UTC(),
		Headers: redactHeaders(r.Header), Body: encodedBody, BodyEncoding: encoding,
		ContentType: r.Header.Get("Content-Type"), Size: int64(len(body)), Truncated: truncated,
	}
	if err := s.store.Add(event); err != nil {
		s.logger.Error("store webhook", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store event")
		return
	}
	w.Header().Set("Location", "/api/events/"+id)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "id": id, "truncated": truncated})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"events": s.store.List(r.URL.Query().Get("channel"))})
	case http.MethodDelete:
		if err := s.store.Clear(); err != nil {
			writeError(w, http.StatusInternalServerError, "could not clear events")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodDelete)
	}
}

func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/events/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		event, err := s.store.Get(id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not read event")
			return
		}
		writeJSON(w, http.StatusOK, event)
	case http.MethodDelete:
		err := s.store.Delete(id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not delete event")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodDelete)
	}
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="webhook-workbench"`)
			writeError(w, http.StatusUnauthorized, "valid bearer token required")
			return
		}
		next(w, r)
	}
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("panic recovered", "value", value)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func redactHeaders(headers http.Header) map[string][]string {
	result := make(map[string][]string, len(headers))
	for key, values := range headers {
		if _, sensitive := sensitiveHeaders[strings.ToLower(key)]; sensitive {
			result[key] = []string{"[REDACTED]"}
			continue
		}
		result[key] = append([]string(nil), values...)
	}
	return result
}

func encodeBody(body []byte) (string, string) {
	if utf8.Valid(body) {
		return string(body), "utf-8"
	}
	return base64.StdEncoding.EncodeToString(body), "base64"
}

func randomID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func remoteHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return address
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
