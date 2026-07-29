package security

import "net/http"

func ValidCSRFToken(r *http.Request, sess *Session) bool {
	if r.Method != http.MethodPost {
		return true
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf_token")
	}
	return token != "" && sess != nil && token == sess.CSRFToken
}
