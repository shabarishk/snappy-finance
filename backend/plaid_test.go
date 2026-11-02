package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoveItemCallsPlaidAndAcceptsSuccessResponse(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"request_id":"request-id"}`))
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

func TestCreateLiabilitiesUpdateLinkTokenRequestsConsentForExistingItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/link/token/create" {
			t.Errorf("Plaid path = %q, want /link/token/create", r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode Plaid request: %v", err)
			return
		}
		if input["access_token"] != "existing-access-token" {
			t.Errorf("access_token = %v, want the existing Item access token", input["access_token"])
		}
		if _, exists := input["products"]; exists {
			t.Error("update-mode request should omit products")
		}
		products, ok := input["additional_consented_products"].([]any)
		if !ok || len(products) != 1 || products[0] != "liabilities" {
			t.Errorf("additional_consented_products = %v, want [liabilities]", input["additional_consented_products"])
		}
		user, ok := input["user"].(map[string]any)
		if !ok || user["client_user_id"] != "snappy-finance-user" {
			t.Errorf("user = %v, want the existing Link client user ID", input["user"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"link_token":"update-link-token"}`))
	}))
	defer server.Close()

	client := &plaidClient{
		clientID: "client",
		secret:   "secret",
		baseURL:  server.URL,
		client:   server.Client(),
	}
	token, err := client.createLiabilitiesUpdateLinkToken(context.Background(), "existing-access-token")
	if err != nil {
		t.Fatalf("createLiabilitiesUpdateLinkToken: %v", err)
	}
	if token != "update-link-token" {
		t.Fatalf("link token = %q, want update-link-token", token)
	}
}

func TestPlaidItemNotFoundIsRecognized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_type":"ITEM_ERROR","error_code":"ITEM_NOT_FOUND","display_message":"Item not found"}`))
	}))
	defer server.Close()

	client := &plaidClient{
		clientID: "client",
		secret:   "secret",
		baseURL:  server.URL,
		client:   server.Client(),
	}
	err := client.removeItem(context.Background(), "access-token")
	if !isPlaidItemNotFound(err) {
		t.Fatalf("isPlaidItemNotFound(%v) = false, want true", err)
	}
}
