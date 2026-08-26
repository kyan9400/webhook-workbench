package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubSignatureVerification(t *testing.T) {
	server, _ := testServer(t, "", 1024)
	body := `{"action":"opened","number":42}`
	secret := "github-webhook-secret"
	capture := httptest.NewRequest(http.MethodPost, "/inbox/github", strings.NewReader(body))
	capture.Header.Set("X-Hub-Signature-256", "sha256="+hmacDigest([]byte(secret), []byte(body)))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, capture)
	if response.Code != http.StatusAccepted {
		t.Fatalf("capture status=%d body=%s", response.Code, response.Body.String())
	}

	verified := verifyRequest(t, server, "event-123", `{"provider":"github","secret":"github-webhook-secret"}`)
	if !verified.Verified || verified.Header != "X-Hub-Signature-256" {
		t.Fatalf("unexpected verification: %+v", verified)
	}
	failed := verifyRequest(t, server, "event-123", `{"provider":"github","secret":"wrong"}`)
	if failed.Verified || failed.Detail != "signature did not match" {
		t.Fatalf("unexpected mismatch: %+v", failed)
	}
}

func TestStripeSignatureVerification(t *testing.T) {
	server, _ := testServer(t, "", 1024)
	body := `{"id":"evt_42","type":"invoice.paid"}`
	secret := "stripe-webhook-secret"
	timestamp := int64(1787709600)
	signed := []byte(fmt.Sprintf("%d.%s", timestamp, body))
	capture := httptest.NewRequest(http.MethodPost, "/inbox/stripe", strings.NewReader(body))
	capture.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=obsolete,v1=%s", timestamp, hmacDigest([]byte(secret), signed)))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, capture)

	verified := verifyRequest(t, server, "event-123", `{"provider":"stripe","secret":"stripe-webhook-secret"}`)
	if !verified.Verified || !strings.Contains(verified.Detail, "receipt window") {
		t.Fatalf("unexpected verification: %+v", verified)
	}
}

func TestGenericSignatureVerification(t *testing.T) {
	server, _ := testServer(t, "", 1024)
	body := `{"event":"custom"}`
	secret := "custom-secret"
	capture := httptest.NewRequest(http.MethodPost, "/inbox/custom", strings.NewReader(body))
	capture.Header.Set("X-Partner-Signature", "v1="+hmacDigest([]byte(secret), []byte(body)))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, capture)

	verified := verifyRequest(t, server, "event-123", `{"provider":"generic","secret":"custom-secret","header":"X-Partner-Signature","prefix":"v1="}`)
	if !verified.Verified || verified.Header != "X-Partner-Signature" {
		t.Fatalf("unexpected verification: %+v", verified)
	}
}

func TestVerificationRejectsTruncatedBodyAndInvalidProvider(t *testing.T) {
	server, _ := testServer(t, "", 4)
	capture := httptest.NewRequest(http.MethodPost, "/inbox/orders", strings.NewReader("12345"))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, capture)

	request := httptest.NewRequest(http.MethodPost, "/api/events/event-123/verify", strings.NewReader(`{"provider":"github","secret":"secret"}`))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", response.Code, response.Body.String())
	}

	server, _ = testServer(t, "", 1024)
	capture = httptest.NewRequest(http.MethodPost, "/inbox/orders", strings.NewReader("{}"))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, capture)
	request = httptest.NewRequest(http.MethodPost, "/api/events/event-123/verify", strings.NewReader(`{"provider":"unknown","secret":"secret"}`))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "provider must be") {
		t.Fatalf("expected provider validation, got %d: %s", response.Code, response.Body.String())
	}
}

func verifyRequest(t *testing.T, server *Server, id, body string) verificationResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/events/"+id+"/verify", strings.NewReader(body))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", response.Code, response.Body.String())
	}
	var result verificationResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
