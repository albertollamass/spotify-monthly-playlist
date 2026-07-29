package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Config struct {
	SessionCookieName        string
	SessionTTL               time.Duration
	CookieSecure             bool
	CookieDomain             string
	TrustedProxyCIDRs        []string
	SessionSecret            string
	TokenEncryptionKeyBase64 string
	RateLimitPerMin          int
}

type Session struct {
	ID          string
	UserID      int64
	SpotifyUser string
	AccessToken string
	CSRFToken   string
	ExpiresAt   time.Time
	TokenExpiry time.Time
}

type pendingEntry struct {
	sessionID string
	expiresAt time.Time
}

type SessionStore struct {
	cfg        Config
	secret     []byte
	mu         sync.RWMutex
	sessions   map[string]Session
	pendingMu  sync.Mutex
	pending    map[string]pendingEntry
	rateMu     sync.Mutex
	rateWindow map[string]rateState
}

type rateState struct {
	windowStart time.Time
	count       int
}

func NewSessionStore(cfg Config) *SessionStore {
	return &SessionStore{
		cfg:        cfg,
		secret:     []byte(cfg.SessionSecret),
		sessions:   make(map[string]Session),
		pending:    make(map[string]pendingEntry),
		rateWindow: make(map[string]rateState),
	}
}

func (s *SessionStore) Create(w http.ResponseWriter, session Session) error {
	if session.ID == "" {
		id, err := randomToken(32)
		if err != nil {
			return err
		}
		session.ID = id
	}
	if session.CSRFToken == "" {
		csrf, err := randomToken(24)
		if err != nil {
			return err
		}
		session.CSRFToken = csrf
	}
	session.ExpiresAt = time.Now().Add(s.cfg.SessionTTL)

	s.mu.Lock()
	s.sessions[session.ID] = session
	s.mu.Unlock()

	cookieValue := s.signSessionID(session.ID)
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.SessionCookieName,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Domain:   s.cfg.CookieDomain,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	return nil
}

// CreatePending stores a session in memory and returns a short-lived one-time
// handoff token. Use ClaimPending on the main server to exchange the token for
// a session cookie. This avoids the cross-origin cookie problem that arises
// when the OAuth callback runs on a different port/host than the main server.
func (s *SessionStore) CreatePending(session Session) (string, error) {
	if session.ID == "" {
		id, err := randomToken(32)
		if err != nil {
			return "", err
		}
		session.ID = id
	}
	if session.CSRFToken == "" {
		csrf, err := randomToken(24)
		if err != nil {
			return "", err
		}
		session.CSRFToken = csrf
	}
	session.ExpiresAt = time.Now().Add(s.cfg.SessionTTL)

	s.mu.Lock()
	s.sessions[session.ID] = session
	s.mu.Unlock()

	token, err := randomToken(32)
	if err != nil {
		return "", err
	}

	s.pendingMu.Lock()
	s.pending[token] = pendingEntry{sessionID: session.ID, expiresAt: time.Now().Add(60 * time.Second)}
	s.pendingMu.Unlock()

	return token, nil
}

// ClaimPending exchanges a one-time handoff token for a session cookie on the
// current response writer. The token is consumed and cannot be reused.
func (s *SessionStore) ClaimPending(w http.ResponseWriter, token string) error {
	s.pendingMu.Lock()
	entry, ok := s.pending[token]
	if ok {
		delete(s.pending, token)
	}
	s.pendingMu.Unlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return errors.New("invalid or expired handoff token")
	}

	s.mu.RLock()
	_, exists := s.sessions[entry.sessionID]
	s.mu.RUnlock()
	if !exists {
		return errors.New("session not found")
	}

	cookieValue := s.signSessionID(entry.sessionID)
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.SessionCookieName,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Domain:   s.cfg.CookieDomain,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	return nil
}

func (s *SessionStore) Update(session Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
}

func (s *SessionStore) Destroy(w http.ResponseWriter, r *http.Request) {
	session, _ := s.Get(r)
	if session != nil {
		s.mu.Lock()
		delete(s.sessions, session.ID)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Domain:   s.cfg.CookieDomain,
		MaxAge:   -1,
	})
}

func (s *SessionStore) Get(r *http.Request) (*Session, error) {
	c, err := r.Cookie(s.cfg.SessionCookieName)
	if err != nil {
		return nil, err
	}
	sessionID, err := s.verifySessionCookie(c.Value)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	session, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil, errors.New("session not found")
	}
	if time.Now().After(session.ExpiresAt) {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		return nil, errors.New("session expired")
	}
	return &session, nil
}

func (s *SessionStore) SignerForTests(sessionID string) string {
	return s.signSessionID(sessionID)
}

func (s *SessionStore) signSessionID(sessionID string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(sessionID))
	sig := mac.Sum(nil)
	raw := sessionID + "." + base64.RawURLEncoding.EncodeToString(sig)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func (s *SessionStore) verifySessionCookie(encoded string) (string, error) {
	rawBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("invalid session encoding")
	}
	raw := string(rawBytes)
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid session format")
	}
	sessionID := parts[0]
	sigProvided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid session signature")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(sessionID))
	expected := mac.Sum(nil)
	if !hmac.Equal(sigProvided, expected) {
		return "", errors.New("session signature mismatch")
	}
	return sessionID, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *SessionStore) AllowRequest(r *http.Request) bool {
	if s.cfg.RateLimitPerMin <= 0 {
		return true
	}
	ip := clientIP(r, s.cfg.TrustedProxyCIDRs)
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	state := s.rateWindow[ip]
	if now.Sub(state.windowStart) >= time.Minute {
		state = rateState{windowStart: now, count: 1}
		s.rateWindow[ip] = state
		return true
	}
	if state.count >= s.cfg.RateLimitPerMin {
		return false
	}
	state.count++
	s.rateWindow[ip] = state
	return true
}

func clientIP(r *http.Request, trustedCIDRs []string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(host)

	for _, cidr := range trustedCIDRs {
		_, subnet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if remoteIP != nil && subnet.Contains(remoteIP) {
			xff := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
			if xff != "" {
				return xff
			}
		}
	}
	if remoteIP == nil {
		return "unknown"
	}
	return remoteIP.String()
}
