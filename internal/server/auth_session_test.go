package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSessionPersistsAcrossAppRestart(t *testing.T) {
	data := t.TempDir()
	a, err := New(data, "test-password-123", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := req(t, a, http.MethodPost, "/api/v1/login", "", Object{"password": "test-password-123"})
	if w.Code != http.StatusOK {
		t.Fatalf("login status %d: %s", w.Code, w.Body.String())
	}
	var loginResult Object
	if err := json.Unmarshal(w.Body.Bytes(), &loginResult); err != nil {
		t.Fatal(err)
	}
	token := str(loginResult["token"])
	if token == "" {
		t.Fatal("login returned empty token")
	}
	expiresAt, err := time.Parse(time.RFC3339, str(loginResult["expires_at"]))
	if err != nil {
		t.Fatalf("invalid expires_at: %v", err)
	}
	if delta := time.Until(expiresAt) - 48*time.Hour; delta < -time.Second || delta > time.Second {
		t.Fatalf("session lifetime = %v, want 48h", 48*time.Hour+delta)
	}
	var storedHash, storedExpiry string
	if err := a.db.QueryRow("SELECT token_hash, expires_at FROM auth_sessions").Scan(&storedHash, &storedExpiry); err != nil {
		t.Fatal(err)
	}
	if storedHash != digest(token) || strings.Contains(storedHash, token) {
		t.Fatal("session table did not store only the token digest")
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = New(data, "test-password-123", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	if got := req(t, a, http.MethodGet, "/api/v1/session", token, nil); got.Code != http.StatusOK {
		t.Fatalf("session after restart status %d: %s", got.Code, got.Body.String())
	}
	var sessionResult Object
	if err := json.Unmarshal(req(t, a, http.MethodGet, "/api/v1/session", token, nil).Body.Bytes(), &sessionResult); err != nil {
		t.Fatal(err)
	}
	if str(sessionResult["expires_at"]) != storedExpiry {
		t.Fatalf("session expiry changed on read: got %v want %v", sessionResult["expires_at"], storedExpiry)
	}
	if got := req(t, a, http.MethodGet, "/api/v1/works", "invalid", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status %d, want 401", got.Code)
	}
	if got := req(t, a, http.MethodPost, "/api/v1/session", token, nil); got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST session status %d, want 405", got.Code)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	a, err := New(t.TempDir(), "test-password-123", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	token := "expired-session-token"
	_, err = a.db.Exec("INSERT INTO auth_sessions(token_hash, expires_at) VALUES(?,?)", digest(token), time.Now().UTC().Add(-time.Second).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if got := req(t, a, http.MethodGet, "/api/v1/session", token, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status %d, want 401", got.Code)
	}
}

func TestSessionDoesNotPersistRawToken(t *testing.T) {
	data := t.TempDir()
	a, err := New(data, "test-password-123", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	token := login(t, a)
	var stored string
	if err := a.db.QueryRow("SELECT token_hash FROM auth_sessions WHERE token_hash=?", digest(token)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("raw bearer token was stored")
	}
}
