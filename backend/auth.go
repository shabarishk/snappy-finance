package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "snappy_session"
const sessionLifetime = 7 * 24 * time.Hour

type principal struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type principalContextKey struct{}

func (a *app) session(w http.ResponseWriter, r *http.Request) {
	user, err := a.resolvePrincipal(r)
	if err != nil {
		log.Printf("resolve session user: %v", err)
		writeError(w, http.StatusInternalServerError, "could not read session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": user != nil,
		"user":          user,
	})
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	username, err := normalizeUsername(input.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	var user principal
	var passwordHash string
	err = a.db.QueryRow(r.Context(), `
		SELECT id, username, role, password_hash
		FROM users
		WHERE username = $1 AND disabled_at IS NULL`, username).
		Scan(&user.ID, &user.Username, &user.Role, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		log.Printf("lookup login user: %v", err)
		writeError(w, http.StatusInternalServerError, "could not sign in")
		return
	}
	if err := a.createSession(r.Context(), w, r, user.ID); err != nil {
		log.Printf("create user session: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user": user})
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		if _, err := a.db.Exec(r.Context(), "DELETE FROM user_sessions WHERE token_hash = $1", a.sessionTokenHash(cookie.Value)); err != nil {
			log.Printf("delete user session: %v", err)
			writeError(w, http.StatusInternalServerError, "could not sign out")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *app) createSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID string) error {
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	expiresAt := time.Now().Add(sessionLifetime)
	if _, err := a.db.Exec(ctx, `
		INSERT INTO user_sessions (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)`, a.sessionTokenHash(token), userID, expiresAt); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionLifetime.Seconds()),
		Expires:  expiresAt,
	})
	return nil
}

func (a *app) sessionTokenHash(token string) []byte {
	mac := hmac.New(sha256.New, a.cfg.sessionSecret)
	_, _ = mac.Write([]byte(token))
	return mac.Sum(nil)
}

func (a *app) resolvePrincipal(r *http.Request) (*principal, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return nil, nil
		}
		return nil, err
	}
	var user principal
	err = a.db.QueryRow(r.Context(), `
		SELECT users.id, users.username, users.role
		FROM user_sessions
		JOIN users ON users.id = user_sessions.user_id
		WHERE user_sessions.token_hash = $1
		  AND user_sessions.expires_at > NOW()
		  AND users.disabled_at IS NULL`, a.sessionTokenHash(cookie.Value)).
		Scan(&user.ID, &user.Username, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func currentPrincipal(r *http.Request) principal {
	user, _ := r.Context().Value(principalContextKey{}).(principal)
	return user
}

func (a *app) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := a.resolvePrincipal(r)
		if err != nil {
			log.Printf("validate user session: %v", err)
			writeError(w, http.StatusServiceUnavailable, "authentication service unavailable")
			return
		}
		if user == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, *user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *app) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if currentPrincipal(r).Role != "admin" {
			writeError(w, http.StatusForbidden, "administrator access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
