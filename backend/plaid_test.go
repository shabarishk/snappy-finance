package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoveItemCallsPlaidAndRequiresConfirmation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/item/remove" {
			t.Errorf("Plaid path = %q, want /item/remove", r.URL.Path)
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode Plaid request: %v", err)
			return
		}
		if input["client_id"] != "client" || input["secret"] != "secret" || input["access_token"] != "access-token" {
			t.Errorf("Plaid request credentials = %v, want configured client, secret, and access token", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"removed":true}`))
	}))
	defer server.Close()

	client := &plaidClient{
		clientID: "client",
		secret:   "secret",
		baseURL:  server.URL,
		client:   server.Client(),
	}
	if err := client.removeItem(context.Background(), "access-token"); err != nil {
		t.Fatalf("removeItem: %v", err)
	}
}

func TestRemoveItemRejectsUnconfirmedRemoval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"removed":false}`))
	}))
	defer server.Close()

	client := &plaidClient{
		clientID: "client",
		secret:   "secret",
		baseURL:  server.URL,
		client:   server.Client(),
	}
	if err := client.removeItem(context.Background(), "access-token"); err == nil {
		t.Fatal("removeItem succeeded without Plaid confirming the removal")
	}
}
