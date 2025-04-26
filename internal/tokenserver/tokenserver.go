package tokenserver

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ilyaluk/girabot/internal/tokencrypto"
)

func GetEncrypted(ctx context.Context, authToken string) (string, error) {
	tok, err := Get(ctx, authToken)
	if err != nil {
		return "", err
	}

	return tokencrypto.Encrypt(tok, authToken)
}

var tokenEndpoint = flag.String("token-url", "http://localhost:8080", "token exchange server base url")

var ErrTokenFetch = fmt.Errorf("firebasetoken: token fetch error")

func Get(ctx context.Context, authToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *tokenEndpoint+"/exchange", nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "girabot (https://t.me/BetterGiraBot)")
	req.Header.Set("X-Gira-Token", authToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("firebasetoken: reading body: %w", err)
	}
	body := string(bodyBytes)

	if strings.Contains(body, "no tokens available") {
		return "", ErrTokenFetch
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("firebasetoken: http %s", resp.Status)
	}

	return body, nil
}

type Stats struct {
	TotalTokens       int64 `json:"total_tokens"`
	ExpiredUnassigned int64 `json:"expired_unassigned"`

	ValidTokens int64 `json:"valid_tokens"`

	AvailableTokens            int64 `json:"available_tokens"`
	AvailableTokensAfter10Mins int64 `json:"available_tokens_after_10_mins"`

	AssignedTokens int64 `json:"assigned_tokens"`
}

func GetStats(ctx context.Context, fbToken string) (*Stats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *tokenEndpoint+"/stats", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "girabot (https://t.me/BetterGiraBot)")
	req.Header.Set("X-Firebase-Token", fbToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res Stats
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("firebasetoken: reading stats: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("firebasetoken: http %s", resp.Status)
	}

	return &res, nil
}
