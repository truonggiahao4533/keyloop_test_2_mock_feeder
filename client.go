package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// errAuth means the API refused our credentials, so retrying cannot help.
var errAuth = errors.New("rejected by the API")

// Result is the API's response to a batch.
type Result struct {
	Inserted int `json:"inserted"`
	Updated  int `json:"updated"`
}

// Client posts batches to POST /v1/ingest/vehicles.
type Client struct {
	BaseURL string
	Token   func() (string, error)
	HTTP    *http.Client
}

// Post sends one batch.
func (c *Client) Post(ctx context.Context, vehicles []Vehicle) (Result, error) {
	body, err := json.Marshal(struct {
		Vehicles []Vehicle `json:"vehicles"`
	}{vehicles})
	if err != nil {
		return Result{}, err
	}
	token, err := c.Token()
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/ingest/vehicles", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	if res.StatusCode == http.StatusOK {
		var out Result
		if err := json.Unmarshal(data, &out); err != nil {
			return Result{}, fmt.Errorf("decode response: %w", err)
		}
		return out, nil
	}
	err = apiError(res.StatusCode, data)
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return Result{}, fmt.Errorf("%w: %w", errAuth, err)
	}
	return Result{}, err
}

func apiError(status int, data []byte) error {
	var e struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) != nil || e.Error.Code == "" {
		return fmt.Errorf("status %d", status)
	}
	return fmt.Errorf("status %d %s: %s (request_id %s)", status, e.Error.Code, e.Error.Message, e.Error.RequestID)
}

// mintToken signs a short-lived HS256 JWT with the ingest role.
func mintToken(secret, subject string, now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"sub":  subject,
		"role": "ingest",
		"iat":  now.Unix(),
		"exp":  now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}
	signed := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
