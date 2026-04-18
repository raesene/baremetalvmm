package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

func (s *sessionStore) create() string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	s.mu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.mu.Unlock()
	return token
}

func (s *sessionStore) valid(token string) bool {
	s.mu.RLock()
	expiry, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		s.delete(token)
		return false
	}
	return true
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{attempts: make(map[string][]time.Time)}
}

func (r *rateLimiter) allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)

	var recent []time.Time
	for _, t := range r.attempts[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	r.attempts[ip] = append(recent, now)
	return len(r.attempts[ip]) <= 5
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "login.html", map[string]interface{}{})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if !s.loginLimiter.allow(ip) {
		s.renderTemplate(w, "login.html", map[string]interface{}{
			"Error": "Too many login attempts. Please wait a minute.",
		})
		return
	}

	password := r.FormValue("password")
	if subtle.ConstantTimeCompare([]byte(password), []byte(s.password)) != 1 {
		s.renderTemplate(w, "login.html", map[string]interface{}{
			"Error": "Invalid password.",
		})
		return
	}

	token := s.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name:     "vmm_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("vmm_session"); err == nil {
		s.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "vmm_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check session cookie
		if cookie, err := r.Cookie("vmm_session"); err == nil {
			if s.sessions.valid(cookie.Value) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Check Authorization header for API access
		if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
			token := auth[7:]
			if s.sessions.valid(token) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Not authenticated
		if isAPIRequest(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		} else {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}
	})
}

func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}

		// Skip CSRF for API requests using Bearer auth
		if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
			next.ServeHTTP(w, r)
			return
		}

		// Check CSRF token from form or header
		token := r.FormValue("csrf_token")
		if token == "" {
			token = r.Header.Get("X-CSRF-Token")
		}

		cookie, err := r.Cookie("vmm_session")
		if err != nil || !s.sessions.valid(token) {
			_ = cookie
			// For HTMX requests, the session token is used as CSRF
			if token == "" || !s.sessions.valid(token) {
				if isAPIRequest(r) || isHTMXRequest(r) {
					http.Error(w, `{"error":"invalid csrf token"}`, http.StatusForbidden)
				} else {
					http.Redirect(w, r, "/login", http.StatusSeeOther)
				}
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func isAPIRequest(r *http.Request) bool {
	return len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/"
}

func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
