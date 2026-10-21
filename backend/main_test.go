package main

import (
	"crypto/hmac"
	"strings"
	"testing"
)

func TestLoadConfigDemoData(t *testing.T) {
	t.Setenv("APP_PASSWORD", "test-password-with-12")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("SESSION_SECRET", "a-session-secret-with-at-least-32-characters")
	t.Setenv("APP_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("DEMO_DATA", "true")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !cfg.demoData {
		t.Fatal("demo data should be enabled")
	}
	if cfg.adminPassword != "test-password-with-12" {
		t.Fatalf("admin password fallback = %q, want APP_PASSWORD", cfg.adminPassword)
	}
}

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

func TestNormalizeUsername(t *testing.T) {
	username, err := normalizeUsername("  Alex_01 ")
	if err != nil {
		t.Fatalf("normalizeUsername: %v", err)
	}
	if username != "alex_01" {
		t.Fatalf("normalized username = %q, want alex_01", username)
	}
	for _, invalid := range []string{"ab", "invalid name", "bad/name", strings.Repeat("x", 33)} {
		if _, err := normalizeUsername(invalid); err == nil {
			t.Errorf("normalizeUsername(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := validatePassword("twelve-characters"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	for _, password := range []string{"short", strings.Repeat("a", 73)} {
		if err := validatePassword(password); err == nil {
			t.Errorf("validatePassword(%q) unexpectedly succeeded", password)
		}
	}
}

func TestSessionTokenHash(t *testing.T) {
	a := &app{cfg: config{sessionSecret: []byte("a-session-secret-that-is-long-enough")}}
	first := a.sessionTokenHash("opaque-token")
	if string(first) == "opaque-token" {
		t.Fatal("session token hash contains plaintext token")
	}
	if !hmac.Equal(first, a.sessionTokenHash("opaque-token")) {
		t.Fatal("same session token produced different hashes")
	}
	if hmac.Equal(first, a.sessionTokenHash("different-token")) {
		t.Fatal("different session tokens produced the same hash")
	}
}
