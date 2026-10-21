package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type dashboardPreferences struct {
	HiddenCards []string `json:"hidden_cards"`
}

type userSummary struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

var dashboardCardIDs = map[string]struct{}{
	"balance":  {},
	"income":   {},
	"spending": {},
	"net":      {},
}

func normalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) < 3 || len(username) > 32 {
		return "", errors.New("must be 3 to 32 characters")
	}
	for _, char := range username {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
			return "", errors.New("use only letters, numbers, dots, dashes, and underscores")
		}
	}
	return username, nil
}

func validatePassword(password string) error {
	if len(password) < 12 {
		return errors.New("must be at least 12 characters")
	}
	if len(password) > 72 {
		return errors.New("must be no more than 72 bytes")
	}
	return nil
}

func bootstrapAdmin(ctx context.Context, db *pgxpool.Pool, username, password string) error {
	var adminExists bool
	if err := db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE role = 'admin' AND disabled_at IS NULL)").Scan(&adminExists); err != nil {
		return err
	}
	if adminExists {
		return nil
	}
	var usersExist bool
	if err := db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&usersExist); err != nil {
		return err
	}
	if usersExist {
		return errors.New("users exist but no active administrator is configured")
	}
	if err := validatePassword(password); err != nil {
		return fmt.Errorf("set ADMIN_PASSWORD for first-run administrator setup: %w", err)
	}
	username, err := normalizeUsername(username)
	if err != nil {
		return fmt.Errorf("ADMIN_USERNAME: %w", err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash administrator password: %w", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, $2, 'admin')`, username, string(passwordHash))
	return err
}

func (a *app) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT id, username, role, created_at
		FROM users
		WHERE disabled_at IS NULL
		ORDER BY username`)
	if err != nil {
		log.Printf("query user list: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load profiles")
		return
	}
	defer rows.Close()
	users := make([]userSummary, 0)
	for rows.Next() {
		var user userSummary
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.CreatedAt); err != nil {
			log.Printf("scan user row: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load profiles")
			return
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate user rows: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load profiles")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (a *app) createUser(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "username must be 3 to 32 characters and use only letters, numbers, dots, dashes, or underscores")
		return
	}
	if err := validatePassword(input.Password); err != nil {
		writeError(w, http.StatusBadRequest, "password must be 12 to 72 characters")
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("hash new user password: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create profile")
		return
	}
	tag, err := a.db.Exec(r.Context(), `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, $2, 'user')
		ON CONFLICT (username) DO NOTHING`, username, string(passwordHash))
	if err != nil {
		log.Printf("create user: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create profile")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "that username is already in use")
		return
	}
	var user userSummary
	if err := a.db.QueryRow(r.Context(), `
		SELECT id, username, role, created_at FROM users WHERE username = $1`, username).
		Scan(&user.ID, &user.Username, &user.Role, &user.CreatedAt); err != nil {
		log.Printf("read created user: %v", err)
		writeError(w, http.StatusInternalServerError, "profile was created but could not be loaded")
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (a *app) getPreferences(w http.ResponseWriter, r *http.Request) {
	user := currentPrincipal(r)
	var encoded []byte
	if err := a.db.QueryRow(r.Context(), "SELECT dashboard_preferences FROM users WHERE id = $1", user.ID).Scan(&encoded); err != nil {
		log.Printf("read dashboard preferences: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard preferences")
		return
	}
	var preferences dashboardPreferences
	if err := json.Unmarshal(encoded, &preferences); err != nil {
		log.Printf("decode dashboard preferences: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard preferences")
		return
	}
	writeJSON(w, http.StatusOK, preferences)
}

func (a *app) updatePreferences(w http.ResponseWriter, r *http.Request) {
	var preferences dashboardPreferences
	if err := decodeJSON(r, &preferences); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	hidden := make([]string, 0, len(preferences.HiddenCards))
	seen := make(map[string]struct{}, len(preferences.HiddenCards))
	for _, card := range preferences.HiddenCards {
		if _, ok := dashboardCardIDs[card]; !ok {
			writeError(w, http.StatusBadRequest, "unknown dashboard card")
			return
		}
		if _, ok := seen[card]; !ok {
			seen[card] = struct{}{}
			hidden = append(hidden, card)
		}
	}
	preferences.HiddenCards = hidden
	encoded, err := json.Marshal(preferences)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save dashboard preferences")
		return
	}
	user := currentPrincipal(r)
	if _, err := a.db.Exec(r.Context(), `
		UPDATE users SET dashboard_preferences = $2::jsonb WHERE id = $1`,
		user.ID, string(encoded)); err != nil {
		log.Printf("save dashboard preferences: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save dashboard preferences")
		return
	}
	writeJSON(w, http.StatusOK, preferences)
}

func (a *app) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := validatePassword(input.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, "new password must be 12 to 72 characters")
		return
	}
	user := currentPrincipal(r)
	var currentHash string
	if err := a.db.QueryRow(r.Context(), "SELECT password_hash FROM users WHERE id = $1", user.ID).Scan(&currentHash); err != nil {
		log.Printf("read current password hash: %v", err)
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(input.CurrentPassword)) != nil {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("hash new password: %v", err)
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), "UPDATE users SET password_hash = $2 WHERE id = $1", user.ID, string(newHash)); err != nil {
		log.Printf("update password hash: %v", err)
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	if _, err := tx.Exec(r.Context(), "DELETE FROM user_sessions WHERE user_id = $1", user.ID); err != nil {
		log.Printf("invalidate user sessions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("commit password change: %v", err)
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	if err := a.createSession(r.Context(), w, r, user.ID); err != nil {
		log.Printf("create session after password change: %v", err)
		writeError(w, http.StatusInternalServerError, "password changed; sign in again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"password_changed": true})
}
