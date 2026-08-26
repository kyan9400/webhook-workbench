package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kyan9400/webhook-workbench/internal/store"
)

const maxReplayResponse = 64 << 10

var errReplayTargetBlocked = errors.New("replay target resolves to a private or local address")

var replayHeadersToDrop = map[string]struct{}{
	"connection":          {},
	"content-length":      {},
	"forwarded":           {},
	"host":                {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"x-forwarded-for":     {},
	"x-forwarded-host":    {},
	"x-forwarded-proto":   {},
}

type replayRequest struct {
	TargetURL string `json:"targetUrl"`
}

type replayResponse struct {
	Status       string `json:"status"`
	StatusCode   int    `json:"statusCode"`
	DurationMS   int64  `json:"durationMs"`
	ContentType  string `json:"contentType,omitempty"`
	Body         string `json:"body"`
	BodyEncoding string `json:"bodyEncoding"`
	Size         int64  `json:"size"`
	Truncated    bool   `json:"truncated"`
}

func newReplayClient(allowPrivate bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split replay address: %w", err)
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve replay target: %w", err)
		}
		for _, address := range addresses {
			if allowPrivate || isPublicReplayIP(address.IP) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			}
		}
		return nil, errReplayTargetBlocked
	}

	client := &http.Client{Transport: transport, Timeout: timeout}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("replay stopped after three redirects")
		}
		return validateReplayURL(request.URL, allowPrivate)
	}
	return client
}

func isPublicReplayIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() &&
		!ip.IsPrivate() &&
		!ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified()
}

func validateReplayURL(target *url.URL, allowPrivate bool) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return errors.New("target URL must use http or https")
	}
	if target.Hostname() == "" {
		return errors.New("target URL must include a hostname")
	}
	if target.User != nil {
		return errors.New("target URL must not include credentials")
	}
	if ip := net.ParseIP(target.Hostname()); ip != nil && !allowPrivate && !isPublicReplayIP(ip) {
		return errReplayTargetBlocked
	}
	return nil
}

func replayBody(event store.Event) ([]byte, error) {
	if event.BodyEncoding == "base64" {
		body, err := base64.StdEncoding.DecodeString(event.Body)
		if err != nil {
			return nil, errors.New("stored event contains invalid base64")
		}
		return body, nil
	}
	return []byte(event.Body), nil
}

func copyReplayHeaders(target http.Header, source map[string][]string) {
	for key, values := range source {
		lower := strings.ToLower(key)
		if _, blocked := replayHeadersToDrop[lower]; blocked {
			continue
		}
		if _, sensitive := sensitiveHeaders[lower]; sensitive {
			continue
		}
		for _, value := range values {
			if value != "[REDACTED]" {
				target.Add(key, value)
			}
		}
	}
}

func (s *Server) replayEvent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	var input replayRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "request body must contain a targetUrl")
		return
	}
	target, err := url.Parse(strings.TrimSpace(input.TargetURL))
	if err != nil {
		writeError(w, http.StatusBadRequest, "target URL is invalid")
		return
	}
	if err := validateReplayURL(target, s.allowPrivateReplay); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	event, err := s.store.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read event")
		return
	}
	body, err := replayBody(event)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), event.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not create replay request")
		return
	}
	copyReplayHeaders(request.Header, event.Headers)
	request.Header.Set("User-Agent", "Webhook-Workbench-Replay/1.1")
	request.Header.Set("X-Webhook-Workbench-Replay", event.ID)

	started := time.Now()
	response, err := s.replayClient.Do(request)
	if err != nil {
		if errors.Is(err, errReplayTargetBlocked) {
			writeError(w, http.StatusBadRequest, errReplayTargetBlocked.Error())
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "replay target timed out")
			return
		}
		s.logger.Warn("replay request failed", "event", event.ID, "targetHost", target.Hostname(), "error", err)
		writeError(w, http.StatusBadGateway, "replay target could not be reached")
		return
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxReplayResponse+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not read replay response")
		return
	}
	truncated := len(responseBody) > maxReplayResponse
	if truncated {
		responseBody = responseBody[:maxReplayResponse]
	}
	encodedBody, encoding := encodeBody(responseBody)
	s.logger.Info("webhook replayed", "event", event.ID, "targetHost", target.Hostname(), "status", response.StatusCode)
	writeJSON(w, http.StatusOK, replayResponse{
		Status: response.Status, StatusCode: response.StatusCode,
		DurationMS: time.Since(started).Milliseconds(), ContentType: response.Header.Get("Content-Type"),
		Body: encodedBody, BodyEncoding: encoding, Size: int64(len(responseBody)), Truncated: truncated,
	})
}
