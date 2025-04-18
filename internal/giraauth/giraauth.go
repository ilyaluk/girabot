package giraauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

type Client struct {
	httpc *http.Client
}

func New(httpc *http.Client) *Client {
	return &Client{httpc: httpc}
}

type tokens struct {
	Access  string `json:"accessToken"`
	Refresh string `json:"refreshToken"`
}

func (c Client) Login(ctx context.Context, email, password string) (*oauth2.Token, error) {
	reqData := map[string]any{
		"Provider": "EmailPassword",
		"CredentialsEmailPassword": map[string]string{
			"Email":    email,
			"Password": password,
		},
	}

	var respData struct {
		Data tokens `json:"data"`
	}

	if err := c.apiCall(ctx, http.MethodPost, "/auth", nil, reqData, &respData); err != nil {
		return nil, err
	}

	return convertTokens(respData.Data)
}

func (c Client) Refresh(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	reqData := map[string]any{
		"Token": refreshToken,
	}

	var respData struct {
		Data tokens `json:"data"`
	}

	if err := c.apiCall(ctx, http.MethodPost, "/token/refresh", nil, reqData, &respData); err != nil {
		return nil, err
	}

	return convertTokens(respData.Data)
}

func (c Client) UserID(ctx context.Context, token string) (string, error) {
	var respData struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`

		Data struct {
			ID string `json:"id"`
			// There are a lot of sensitive fields in the response, but we only need the ID
		} `json:"data"`
	}

	hdr := http.Header{
		"Authorization": []string{"Bearer " + token},
	}
	if err := c.apiCall(ctx, http.MethodGet, "/user", hdr, nil, &respData); err != nil {
		return "", err
	}

	if respData.Error.Code != 0 {
		return "", fmt.Errorf("giraauth: %s (%d)", respData.Error.Message, respData.Error.Code)
	}

	return respData.Data.ID, nil
}

func convertTokens(ts tokens) (*oauth2.Token, error) {
	var claims jwt.RegisteredClaims
	_, _, err := jwt.NewParser().ParseUnverified(ts.Access, &claims)
	if err != nil {
		return nil, fmt.Errorf("giraauth: parsing access token: %w", err)
	}

	return &oauth2.Token{
		AccessToken:  ts.Access,
		RefreshToken: ts.Refresh,
		Expiry:       claims.ExpiresAt.Time,
	}, nil
}

var (
	ErrInternalServer      = fmt.Errorf("giraauth: internal server error")
	ErrInvalidEmail        = fmt.Errorf("giraauth: invalid email")
	ErrInvalidCredentials  = fmt.Errorf("giraauth: invalid credentials")
	ErrInvalidRefreshToken = fmt.Errorf("giraauth: invalid refresh token")
)

func (c Client) apiCall(ctx context.Context, method, api string, headers http.Header, reqVal, respVal any) error {
	var reqData []byte
	var err error
	if reqVal != nil {
		reqData, err = json.Marshal(reqVal)
		if err != nil {
			return fmt.Errorf("giraauth: marshaling request: %w", err)
		}
	}

	path := fmt.Sprintf("https://c2g091p01.emel.pt/auth%s", api)
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewBuffer(reqData))
	if err != nil {
		return fmt.Errorf("giraauth: creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gira/3.4.3 (Android 34)")

	if headers != nil {
		for k, v := range headers {
			req.Header.Set(k, v[0])
		}
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("giraauth: performing request: %w", err)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("giraauth: reading body: %w", err)
	}

	if resp.StatusCode == http.StatusInternalServerError {
		return ErrInternalServer
	}

	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "Invalid refresh token") {
		return ErrInvalidRefreshToken
	}

	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "The field Email must") {
		return ErrInvalidEmail
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("giraauth: http %s '%s'", resp.Status, string(body))
	}

	var errorVal struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(bytes.NewBuffer(body)).Decode(&errorVal); err != nil {
		return fmt.Errorf("giraauth: decoding error: %w", err)
	}

	if errorVal.Error.Code == 100 && errorVal.Error.Message == "Invalid credentials." {
		return ErrInvalidCredentials
	}

	if errorVal.Error.Code != 0 {
		return fmt.Errorf("giraauth: %s (%d)", errorVal.Error.Message, errorVal.Error.Code)
	}

	if respVal != nil {
		return json.NewDecoder(bytes.NewBuffer(body)).Decode(respVal)
	}

	return nil
}
