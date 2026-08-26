package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kyan9400/webhook-workbench/internal/store"
)

const (
	maxVerificationRequest = 8 << 10
	maxVerificationSecret  = 4 << 10
	stripeTolerance        = 5 * time.Minute
)

type verificationRequest struct {
	Provider string `json:"provider"`
	Secret   string `json:"secret"`
	Header   string `json:"header,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
}

type verificationResponse struct {
	Verified  bool   `json:"verified"`
	Provider  string `json:"provider"`
	Algorithm string `json:"algorithm"`
	Header    string `json:"header"`
	Detail    string `json:"detail"`
}

func (s *Server) verifyEvent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxVerificationRequest)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input verificationRequest
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "request body must contain a provider and secret")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	if input.Secret == "" || len(input.Secret) > maxVerificationSecret {
		writeError(w, http.StatusBadRequest, "secret must contain 1-4096 bytes")
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
	if event.Truncated {
		writeError(w, http.StatusUnprocessableEntity, "a truncated body cannot be verified")
		return
	}
	body, err := replayBody(event)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	result, err := verifySignature(event, body, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func verifySignature(event store.Event, body []byte, input verificationRequest) (verificationResponse, error) {
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	switch provider {
	case "github":
		const header = "X-Hub-Signature-256"
		expected := "sha256=" + hmacDigest([]byte(input.Secret), body)
		verified := secureStringEqual(storedHeader(event.Headers, header), expected)
		return verificationResult(verified, provider, header), nil
	case "stripe":
		const header = "Stripe-Signature"
		verified, detail := verifyStripe(storedHeader(event.Headers, header), []byte(input.Secret), body, event.ReceivedAt)
		return verificationResponse{
			Verified: verified, Provider: provider, Algorithm: "HMAC-SHA-256",
			Header: header, Detail: detail,
		}, nil
	case "generic":
		header := strings.TrimSpace(input.Header)
		if !validHeaderName(header) {
			return verificationResponse{}, errors.New("generic verification requires a valid signature header name")
		}
		if len(input.Prefix) > 64 {
			return verificationResponse{}, errors.New("signature prefix must contain at most 64 characters")
		}
		expected := input.Prefix + hmacDigest([]byte(input.Secret), body)
		verified := secureStringEqual(storedHeader(event.Headers, header), expected)
		return verificationResult(verified, provider, header), nil
	default:
		return verificationResponse{}, errors.New("provider must be github, stripe, or generic")
	}
}

func verificationResult(verified bool, provider, header string) verificationResponse {
	detail := "signature did not match"
	if verified {
		detail = "signature matched the captured body"
	}
	return verificationResponse{
		Verified: verified, Provider: provider, Algorithm: "HMAC-SHA-256",
		Header: header, Detail: detail,
	}
}

func verifyStripe(header string, secret, body []byte, receivedAt time.Time) (bool, string) {
	var timestamp int64
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch key {
		case "t":
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err == nil {
				timestamp = parsed
			}
		case "v1":
			signatures = append(signatures, value)
		}
	}
	if timestamp == 0 || len(signatures) == 0 {
		return false, "Stripe-Signature is missing a timestamp or v1 digest"
	}
	age := receivedAt.Sub(time.Unix(timestamp, 0))
	if age < -stripeTolerance || age > stripeTolerance {
		return false, "signature timestamp falls outside the five-minute receipt window"
	}
	payload := append([]byte(fmt.Sprintf("%d.", timestamp)), body...)
	expected := hmacDigest(secret, payload)
	for _, signature := range signatures {
		if secureHexEqual(signature, expected) {
			return true, "signature matched the captured body and receipt window"
		}
	}
	return false, "signature did not match"
}

func hmacDigest(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func secureStringEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func secureHexEqual(left, right string) bool {
	leftBytes, leftErr := hex.DecodeString(left)
	rightBytes, rightErr := hex.DecodeString(right)
	return leftErr == nil && rightErr == nil && len(leftBytes) == len(rightBytes) &&
		subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}

func storedHeader(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func validHeaderName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	const symbols = "!#$%&'*+-.^_`|~"
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune(symbols, character) {
			continue
		}
		return false
	}
	return true
}
