package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type plaidClient struct {
	clientID string
	secret   string
	baseURL  string
	client   *http.Client
}

type plaidAPIError struct {
	ErrorType string
	ErrorCode string
	Display   string
}

func (e *plaidAPIError) Error() string {
	return fmt.Sprintf("Plaid %s: %s (%s)", e.ErrorType, e.ErrorCode, e.Display)
}

func isPlaidErrorCode(err error, code string) bool {
	var plaidErr *plaidAPIError
	return errors.As(err, &plaidErr) && plaidErr.ErrorCode == code
}

func isPlaidItemNotFound(err error) bool {
	return isPlaidErrorCode(err, "ITEM_NOT_FOUND")
}

func newPlaidClient(cfg config) *plaidClient {
	baseURL := "https://sandbox.plaid.com"
	if cfg.plaidEnv == "development" {
		baseURL = "https://development.plaid.com"
	} else if cfg.plaidEnv == "production" {
		baseURL = "https://production.plaid.com"
	}
	return &plaidClient{
		clientID: cfg.plaidClientID,
		secret:   cfg.plaidSecret,
		baseURL:  baseURL,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *plaidClient) configured() bool {
	return p.clientID != "" && p.secret != ""
}

func (p *plaidClient) post(ctx context.Context, path string, input, output any) error {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return err
	}
	payload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(inputJSON, &payload); err != nil {
		return err
	}
	payload["client_id"], _ = json.Marshal(p.clientID)
	payload["secret"], _ = json.Marshal(p.secret)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var plaidError struct {
			ErrorCode string `json:"error_code"`
			ErrorType string `json:"error_type"`
			Display   string `json:"display_message"`
		}
		_ = json.Unmarshal(responseBody, &plaidError)
		return &plaidAPIError{
			ErrorType: plaidError.ErrorType,
			ErrorCode: plaidError.ErrorCode,
			Display:   plaidError.Display,
		}
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode Plaid response: %w", err)
	}
	return nil
}

func (p *plaidClient) createLinkToken(ctx context.Context) (string, error) {
	var result struct {
		LinkToken string `json:"link_token"`
	}
	err := p.post(ctx, "/link/token/create", map[string]any{
		"client_name":       "Snappy Finance",
		"language":          "en",
		"country_codes":     []string{"US"},
		"user":              map[string]string{"client_user_id": "snappy-finance-user"},
		"products":          []string{"transactions"},
		"optional_products": []string{"liabilities"},
	}, &result)
	return result.LinkToken, err
}

type exchangeResponse struct {
	AccessToken string `json:"access_token"`
	ItemID      string `json:"item_id"`
}

func (p *plaidClient) exchangePublicToken(ctx context.Context, publicToken string) (exchangeResponse, error) {
	var result exchangeResponse
	err := p.post(ctx, "/item/public_token/exchange", map[string]string{"public_token": publicToken}, &result)
	return result, err
}

type plaidAccount struct {
	AccountID    string  `json:"account_id"`
	Name         string  `json:"name"`
	OfficialName *string `json:"official_name"`
	Mask         *string `json:"mask"`
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	Balances     struct {
		Current         *float64 `json:"current"`
		IsoCurrencyCode *string  `json:"iso_currency_code"`
	} `json:"balances"`
}

func (p *plaidClient) accountsGet(ctx context.Context, accessToken string) (struct {
	Accounts []plaidAccount `json:"accounts"`
}, error) {
	var result struct {
		Accounts []plaidAccount `json:"accounts"`
	}
	err := p.post(ctx, "/accounts/get", map[string]string{"access_token": accessToken}, &result)
	return result, err
}

type plaidCreditLiability struct {
	AccountID              string   `json:"account_id"`
	LastStatementBalance   *float64 `json:"last_statement_balance"`
	LastStatementIssueDate *string  `json:"last_statement_issue_date"`
	NextPaymentDueDate     *string  `json:"next_payment_due_date"`
}

func (p *plaidClient) liabilitiesGet(ctx context.Context, accessToken string) ([]plaidCreditLiability, error) {
	var result struct {
		Liabilities struct {
			Credit []plaidCreditLiability `json:"credit"`
		} `json:"liabilities"`
	}
	err := p.post(ctx, "/liabilities/get", map[string]string{"access_token": accessToken}, &result)
	return result.Liabilities.Credit, err
}

type plaidTransaction struct {
	TransactionID           string  `json:"transaction_id"`
	AccountID               string  `json:"account_id"`
	Name                    string  `json:"name"`
	MerchantName            *string `json:"merchant_name"`
	Amount                  float64 `json:"amount"`
	Date                    string  `json:"date"`
	Pending                 bool    `json:"pending"`
	IsoCurrencyCode         *string `json:"iso_currency_code"`
	PersonalFinanceCategory struct {
		Primary  *string `json:"primary"`
		Detailed *string `json:"detailed"`
	} `json:"personal_finance_category"`
}

type syncResponse struct {
	Added    []plaidTransaction `json:"added"`
	Modified []plaidTransaction `json:"modified"`
	Removed  []struct {
		TransactionID string `json:"transaction_id"`
	} `json:"removed"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

func (p *plaidClient) transactionsSync(ctx context.Context, accessToken, cursor string) (syncResponse, error) {
	input := map[string]any{"access_token": accessToken, "count": 500}
	if cursor != "" {
		input["cursor"] = cursor
	}
	var result syncResponse
	err := p.post(ctx, "/transactions/sync", input, &result)
	return result, err
}

func (p *plaidClient) removeItem(ctx context.Context, accessToken string) error {
	var result struct {
		RequestID string `json:"request_id"`
	}
	if err := p.post(ctx, "/item/remove", map[string]string{"access_token": accessToken}, &result); err != nil {
		return err
	}
	return nil
}
