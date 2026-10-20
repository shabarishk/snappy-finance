package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	encrypted, err := encrypt(key, "access-token-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if string(encrypted) == "access-token-value" {
		t.Fatal("ciphertext must not contain the plaintext token")
	}
	decrypted, err := decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != "access-token-value" {
		t.Fatalf("decrypted value = %q, want original token", decrypted)
	}

	encrypted[len(encrypted)-1] ^= 1
	if _, err := decrypt(key, encrypted); err == nil {
		t.Fatal("decrypt accepted tampered ciphertext")
	}
}

func TestSessionCookieAuthentication(t *testing.T) {
	a := &app{
		cfg:       config{sessionSecret: []byte("a-session-secret-that-is-long-enough")},
		sessionID: "test-user",
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: "snappy_session", Value: a.sessionCookie()})
	if !a.isAuthenticated(request) {
		t.Fatal("valid session cookie was rejected")
	}

	request = httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: "snappy_session", Value: "1.invalid"})
	if a.isAuthenticated(request) {
		t.Fatal("expired or invalid session cookie was accepted")
	}
}
