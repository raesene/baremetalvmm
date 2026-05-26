package web

import (
	"net/http"
	"testing"
)

func TestRateLimiter_AllowsInitialRequests(t *testing.T) {
	rl := newRateLimiter()

	for i := 0; i < 5; i++ {
		if !rl.allow("1.2.3.4") {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
}

func TestRateLimiter_BlocksExcessRequests(t *testing.T) {
	rl := newRateLimiter()

	for i := 0; i < 5; i++ {
		rl.allow("1.2.3.4")
	}

	if rl.allow("1.2.3.4") {
		t.Error("6th request should be blocked")
	}
}

func TestRateLimiter_SeparateKeysAreIndependent(t *testing.T) {
	rl := newRateLimiter()

	for i := 0; i < 5; i++ {
		rl.allow("1.1.1.1")
	}

	if !rl.allow("2.2.2.2") {
		t.Error("different IP should not be rate limited")
	}
}

func TestSessionStore_CreateAndValidate(t *testing.T) {
	ss := newSessionStore()

	token := ss.create()
	if token == "" {
		t.Fatal("token should not be empty")
	}
	if !ss.valid(token) {
		t.Error("newly created token should be valid")
	}
}

func TestSessionStore_InvalidToken(t *testing.T) {
	ss := newSessionStore()

	if ss.valid("nonexistent") {
		t.Error("nonexistent token should not be valid")
	}
	if ss.valid("") {
		t.Error("empty token should not be valid")
	}
}

func TestSessionStore_Delete(t *testing.T) {
	ss := newSessionStore()

	token := ss.create()
	ss.delete(token)
	if ss.valid(token) {
		t.Error("deleted token should not be valid")
	}
}

func TestSessionStore_UniqueTokens(t *testing.T) {
	ss := newSessionStore()
	tokens := make(map[string]bool)

	for i := 0; i < 50; i++ {
		token := ss.create()
		if tokens[token] {
			t.Fatalf("duplicate token: %s", token)
		}
		tokens[token] = true
	}
}

func TestSessionStore_CSRFToken(t *testing.T) {
	ss := newSessionStore()

	sessionToken := ss.create()
	csrfToken := ss.csrfToken(sessionToken)
	if csrfToken == "" {
		t.Fatal("CSRF token should not be empty")
	}
	if csrfToken == sessionToken {
		t.Error("CSRF token must differ from session token")
	}
}

func TestSessionStore_CSRFTokenForInvalidSession(t *testing.T) {
	ss := newSessionStore()

	if csrf := ss.csrfToken("nonexistent"); csrf != "" {
		t.Error("CSRF token for nonexistent session should be empty")
	}
}

func TestSessionStore_ValidCSRF(t *testing.T) {
	ss := newSessionStore()

	sessionToken := ss.create()
	csrfToken := ss.csrfToken(sessionToken)

	if !ss.validCSRF(sessionToken, csrfToken) {
		t.Error("valid session+CSRF pair should pass")
	}
}

func TestSessionStore_ValidCSRF_WrongCSRF(t *testing.T) {
	ss := newSessionStore()

	sessionToken := ss.create()

	if ss.validCSRF(sessionToken, "wrong-csrf-token") {
		t.Error("wrong CSRF token should fail")
	}
}

func TestSessionStore_ValidCSRF_WrongSession(t *testing.T) {
	ss := newSessionStore()

	ss.create()

	if ss.validCSRF("wrong-session", "any-csrf") {
		t.Error("wrong session token should fail")
	}
}

func TestSessionStore_ValidCSRF_SessionTokenAsCSRF(t *testing.T) {
	ss := newSessionStore()

	sessionToken := ss.create()

	if ss.validCSRF(sessionToken, sessionToken) {
		t.Error("session token used as CSRF token should fail")
	}
}

func TestSessionStore_ValidCSRF_DeletedSession(t *testing.T) {
	ss := newSessionStore()

	sessionToken := ss.create()
	csrfToken := ss.csrfToken(sessionToken)
	ss.delete(sessionToken)

	if ss.validCSRF(sessionToken, csrfToken) {
		t.Error("deleted session should fail CSRF validation")
	}
}

func TestIsAPIRequest(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/api/v1/vms", true},
		{"/api/health", true},
		{"/api/", true},
		{"/dashboard", false},
		{"/login", false},
		{"/vms", false},
		{"/", false},
		{"/ap", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r, _ := http.NewRequest("GET", tt.path, nil)
			if got := isAPIRequest(r); got != tt.want {
				t.Errorf("isAPIRequest(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsHTMXRequest(t *testing.T) {
	t.Run("with HX-Request header", func(t *testing.T) {
		r, _ := http.NewRequest("GET", "/", nil)
		r.Header.Set("HX-Request", "true")
		if !isHTMXRequest(r) {
			t.Error("should detect HTMX request")
		}
	})

	t.Run("without HX-Request header", func(t *testing.T) {
		r, _ := http.NewRequest("GET", "/", nil)
		if isHTMXRequest(r) {
			t.Error("should not detect non-HTMX request")
		}
	})
}
