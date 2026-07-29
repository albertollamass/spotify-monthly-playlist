package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFHeaderFlow(t *testing.T) {
	s := &Session{CSRFToken: "abc123"}
	req := httptest.NewRequest(http.MethodPost, "/sync/manual", nil)
	req.Header.Set("X-CSRF-Token", "abc123")
	if !ValidCSRFToken(req, s) {
		t.Fatalf("ValidCSRFToken should accept matching header token")
	}
}

func TestCSRFMismatch(t *testing.T) {
	s := &Session{CSRFToken: "abc123"}
	req := httptest.NewRequest(http.MethodPost, "/sync/manual", nil)
	req.Header.Set("X-CSRF-Token", "bad")
	if ValidCSRFToken(req, s) {
		t.Fatalf("ValidCSRFToken should reject mismatched token")
	}
}
