package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Formance implements the pinned Ledger v2.4.16 HTTP API. It never starts a new
// business operation after an uncertain write; the caller owns reconciliation.
type Formance struct {
	baseURL    string
	ledgerName string
	client     *http.Client
}

var _ Ledger = (*Formance)(nil)

func NewFormance(baseURL, ledgerName string, client *http.Client) *Formance {
	var httpClient http.Client
	if client != nil {
		httpClient = *client
	}
	if httpClient.Timeout <= 0 {
		httpClient.Timeout = 10 * time.Second
	}
	// A redirected write must not silently become a GET or target another ledger.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Formance{baseURL: strings.TrimRight(baseURL, "/"), ledgerName: ledgerName, client: &httpClient}
}

// Ensure creates the logical ledger, or reads back an already-existing ledger.
func (f *Formance) Ensure(ctx context.Context) error {
	status, body, err := f.request(ctx, http.MethodPost, "", nil, []byte(`{}`), "")
	if err != nil {
		return err
	}
	if status == http.StatusNoContent {
		return nil
	}
	var rejection apiError
	if status != http.StatusBadRequest || json.Unmarshal(body, &rejection) != nil || rejection.Code != "LEDGER_ALREADY_EXISTS" {
		return classifyError(status, body)
	}
	status, body, err = f.request(ctx, http.MethodGet, "", nil, nil, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return classifyError(status, body)
	}
	var response struct {
		Data *struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Data == nil || response.Data.Name != f.ledgerName {
		return fmt.Errorf("%w: invalid ledger readback", ErrUnknown)
	}
	return nil
}

func (f *Formance) Post(ctx context.Context, command Command) (Transaction, error) {
	if command.Reference == "" || len(command.Postings) == 0 {
		return Transaction{}, fmt.Errorf("%w: reference and postings are required", ErrConflict)
	}
	for _, p := range command.Postings {
		if p.Source == "" || p.Destination == "" || p.Asset == "" || p.Amount <= 0 {
			return Transaction{}, fmt.Errorf("%w: invalid posting", ErrConflict)
		}
	}
	payload := struct {
		Command
		Metadata map[string]string `json:"metadata"`
	}{Command: command, Metadata: map[string]string{}}
	body, err := json.Marshal(payload)
	if err != nil {
		return Transaction{}, fmt.Errorf("%w: encode command: %w", ErrConflict, err)
	}
	// A changed command under one business reference must conflict, not create a new write.
	hash := sha256.Sum256([]byte(f.ledgerName + "\x00" + command.Reference))
	key := "unihub:tx:" + hex.EncodeToString(hash[:])
	status, body, err := f.request(ctx, http.MethodPost, "/transactions", nil, body, key)
	if err != nil {
		return Transaction{}, err
	}
	if status != http.StatusOK {
		return Transaction{}, classifyError(status, body)
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Transaction{}, fmt.Errorf("%w: invalid-success-response: %w", ErrUnknown, err)
	}
	tx, err := decodeTransaction(response.Data, command.Reference)
	if err != nil {
		return Transaction{}, err
	}
	if !slices.Equal(tx.Postings, command.Postings) {
		return Transaction{}, fmt.Errorf("%w: invalid-success-response: postings differ from command", ErrUnknown)
	}
	return tx, nil
}

func (f *Formance) Find(ctx context.Context, reference string) (*Transaction, error) {
	if reference == "" {
		return nil, fmt.Errorf("%w: reference is required", ErrConflict)
	}
	query := matchQuery("reference", reference)
	query.Set("pageSize", "2")
	status, body, err := f.request(ctx, http.MethodGet, "/transactions", query, nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, classifyError(status, body)
	}
	var response struct {
		Cursor *struct {
			Data    []json.RawMessage `json:"data"`
			HasMore bool              `json:"hasMore"`
		} `json:"cursor"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Cursor == nil || response.Cursor.Data == nil || response.Cursor.HasMore || len(response.Cursor.Data) > 1 {
		return nil, fmt.Errorf("%w: invalid-success-response: invalid transaction cursor", ErrUnknown)
	}
	if len(response.Cursor.Data) == 0 {
		return nil, nil
	}
	tx, err := decodeTransaction(response.Cursor.Data[0], reference)
	if err != nil {
		return nil, err
	}
	return &tx, nil
}

func (f *Formance) Balance(ctx context.Context, address, asset string) (int64, error) {
	if address == "" || asset == "" {
		return 0, fmt.Errorf("%w: address and asset are required", ErrConflict)
	}
	status, body, err := f.request(ctx, http.MethodGet, "/aggregate/balances", matchQuery("address", address), nil, "")
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, classifyError(status, body)
	}
	var response struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Data == nil {
		return 0, fmt.Errorf("%w: invalid-success-response: invalid balances", ErrUnknown)
	}
	raw, exists := response.Data[asset]
	if !exists {
		return 0, nil
	}
	text, err := integerText(raw, true)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid-success-response: invalid balance: %w", ErrUnknown, err)
	}
	amount, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid-success-response: balance overflow: %w", ErrUnknown, err)
	}
	return amount, nil
}

func matchQuery(field, value string) url.Values {
	body, _ := json.Marshal(map[string]any{"$match": map[string]string{field: value}})
	return url.Values{"query": []string{string(body)}}
}

func (f *Formance) request(ctx context.Context, method, path string, query url.Values, body []byte, key string) (int, []byte, error) {
	endpoint, err := url.Parse(f.baseURL + "/v2/" + url.PathEscape(f.ledgerName) + path)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || f.ledgerName == "" {
		return 0, nil, fmt.Errorf("%w: invalid Ledger endpoint", ErrUnknown)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: create request: %w", ErrUnknown, err)
	}
	if method == http.MethodPost {
		// net/http otherwise treats Idempotency-Key plus GetBody as permission to
		// retry a write on a reused connection. Recovery belongs to the caller.
		req.GetBody = nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Formance-Bigint-As-String", "true")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := f.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: HTTP request: %w", ErrUnknown, err)
	}
	defer response.Body.Close()
	const maxResponseBytes = 4 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("%w: read response: %w", ErrUnknown, err)
	}
	if len(responseBody) > maxResponseBytes {
		return response.StatusCode, nil, fmt.Errorf("%w: response exceeds limit", ErrUnknown)
	}
	return response.StatusCode, responseBody, nil
}

type apiError struct {
	Code    string `json:"errorCode"`
	Message string `json:"errorMessage"`
}

func classifyError(status int, body []byte) error {
	var rejection apiError
	if err := json.Unmarshal(body, &rejection); err != nil {
		return fmt.Errorf("%w: HTTP %d with invalid error response", ErrUnknown, status)
	}
	kind := ErrUnknown
	switch {
	case status == http.StatusBadRequest && rejection.Code == "INSUFFICIENT_FUND":
		kind = ErrInsufficientFunds
	case status == http.StatusConflict && rejection.Code == "CONFLICT":
		kind = ErrConflict
	case status == http.StatusBadRequest && rejection.Code == "VALIDATION":
		kind = ErrConflict
	}
	return fmt.Errorf("%w: HTTP %d (%s): %s", kind, status, rejection.Code, rejection.Message)
}

func decodeTransaction(data json.RawMessage, reference string) (Transaction, error) {
	var wire struct {
		ID        json.RawMessage `json:"id"`
		Reference string          `json:"reference"`
		Postings  []struct {
			Source      string          `json:"source"`
			Destination string          `json:"destination"`
			Asset       string          `json:"asset"`
			Amount      json.RawMessage `json:"amount"`
		} `json:"postings"`
		Reverted bool `json:"reverted"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Transaction{}, fmt.Errorf("%w: invalid-success-response: %w", ErrUnknown, err)
	}
	id, err := integerText(wire.ID, false)
	if err != nil {
		return Transaction{}, fmt.Errorf("%w: invalid-success-response: missing or invalid transaction ID", ErrUnknown)
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return Transaction{}, fmt.Errorf("%w: invalid-success-response: transaction ID overflow", ErrUnknown)
	}
	if wire.Reference != reference || len(wire.Postings) == 0 || wire.Reverted {
		return Transaction{}, fmt.Errorf("%w: invalid-success-response: invalid transaction reference, postings or reverted state", ErrUnknown)
	}
	tx := Transaction{ID: id, Reference: wire.Reference, Postings: make([]Posting, 0, len(wire.Postings))}
	for _, p := range wire.Postings {
		text, err := integerText(p.Amount, false)
		if err != nil {
			return Transaction{}, fmt.Errorf("%w: invalid-success-response: invalid posting amount", ErrUnknown)
		}
		amount, err := strconv.ParseInt(text, 10, 64)
		if err != nil || amount <= 0 || p.Source == "" || p.Destination == "" || p.Asset == "" {
			return Transaction{}, fmt.Errorf("%w: invalid-success-response: invalid posting", ErrUnknown)
		}
		tx.Postings = append(tx.Postings, Posting{Source: p.Source, Destination: p.Destination, Asset: p.Asset, Amount: amount})
	}
	return tx, nil
}

// integerText accepts integer JSON tokens and decimal strings, never float64.
func integerText(raw json.RawMessage, signed bool) (string, error) {
	text := string(raw)
	if len(text) > 0 && text[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
	}
	digits := text
	if signed && strings.HasPrefix(digits, "-") {
		digits = digits[1:]
	}
	if digits == "" {
		return "", fmt.Errorf("missing integer")
	}
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return "", fmt.Errorf("not a decimal integer")
		}
	}
	return text, nil
}
