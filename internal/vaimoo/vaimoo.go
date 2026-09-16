// Package vaimoo talks to the VAIMOO backend that runs Gira since the 2026
// migration, and to the EMEL login service that authenticates against it.
package vaimoo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ilyaluk/girabot/internal/retryablehttp"
)

const (
	// BaseURL is the VAIMOO API that serves trips, the wallet and the account.
	BaseURL = "https://emel-consumerapp.vaimoo.com/"
	// EmelLoginURL is the EMEL identity provider that owns the credentials.
	EmelLoginURL = "https://login.emel.pt/"

	// AppID identifies the official Gira app to VAIMOO.
	AppID = "8d75593b-83a1-4cce-862f-1671b59c5b0f"
	// AppVersion is sent with every call as the mainAppVersion parameter.
	AppVersion = "A1.0.0"
	// RedirectURI is the deep link the EMEL code exchange is bound to.
	RedirectURI = "vaimoo://auth/callback"

	// Tenant scopes Gira data inside the VAIMOO project, which is shared with
	// other cities.
	Tenant = "P1/EML/EML/"

	// accessTokenLifetime is how long a token lasts when the server does not say.
	accessTokenLifetime = 5 * time.Minute
)

// Client calls the VAIMOO and EMEL APIs.
type Client struct {
	httpc *http.Client
}

// New returns a client using httpc, wrapped in the retrying transport.
func New(httpc *http.Client) *Client {
	client := *httpc
	client.Transport = retryablehttp.NewTransport(httpc.Transport)
	return &Client{httpc: &client}
}

var (
	// ErrInvalidCredentials means EMEL rejected the email and password pair.
	ErrInvalidCredentials = errors.New("vaimoo: invalid credentials")
	// ErrInvalidEmail means the email is not shaped like one.
	ErrInvalidEmail = errors.New("vaimoo: invalid email")
	// ErrInvalidRefreshToken means the session can only be restored by logging in.
	ErrInvalidRefreshToken = errors.New("vaimoo: invalid refresh token")
	// ErrInternalServer means the backend failed in a way worth retrying later.
	ErrInternalServer = errors.New("vaimoo: internal server error")
	// ErrUnauthorized means the access token was rejected.
	ErrUnauthorized = errors.New("vaimoo: unauthorized")
)

// Error is a failure the VAIMOO API reported in its own envelope.
type Error struct {
	Status  int
	Code    int
	Message string
	Body    string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("vaimoo: http %d: %s (%d)", e.Status, e.Message, e.Code)
	}
	// A body with no recognisable message can be anything, so only a prefix of
	// it goes into an error that may be shown to a rider.
	body := e.Body
	if len(body) > 200 {
		body = body[:200] + "..."
	}
	return fmt.Sprintf("vaimoo: http %d: %s", e.Status, body)
}

// options are the parts of a request that vary between endpoints.
type options struct {
	method  string
	token   string
	userID  int64
	params  map[string]string
	headers map[string]string
	body    any
}

// call performs a request against the VAIMOO API and decodes the answer.
func (c *Client) call(ctx context.Context, path string, opts options, out any) error {
	params := url.Values{}
	params.Set("userId", userIDParam(opts.userID))
	params.Set("mainAppVersion", AppVersion)
	params.Set("t", strconv.FormatInt(time.Now().UnixMilli(), 10))
	for k, v := range opts.params {
		params.Set(k, v)
	}

	headers := map[string]string{
		"Accept":          "application/json",
		"Accept-Language": "en",
		"AppId":           AppID,
		"Authorization":   opts.token,
	}
	for k, v := range opts.headers {
		headers[k] = v
	}

	return c.request(ctx, opts.method, BaseURL+path+"?"+params.Encode(), headers, opts.body, out)
}

// userIDParam renders the userId query parameter, which the app sends as the
// literal "null" before it knows the rider's id.
func userIDParam(id int64) string {
	if id == 0 {
		return "null"
	}
	return strconv.FormatInt(id, 10)
}

func (c *Client) request(ctx context.Context, method, endpoint string, headers map[string]string, body, out any) error {
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vaimoo: marshaling request: %w", err)
		}
	}

	if method == "" {
		method = http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("vaimoo: creating request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("vaimoo: performing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("vaimoo: reading body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp.StatusCode, respBody)
	}

	if out == nil {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(respBody))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("vaimoo: decoding response: %w", err)
	}
	return nil
}

// responseError turns a failed response into an error, unwrapping the VAIMOO
// envelope so callers can match on the backend's own message.
func responseError(status int, body []byte) error {
	if status == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if status >= 500 {
		return ErrInternalServer
	}

	var envelope struct {
		ResponseStatus struct {
			ErrorCode int    `json:"errorCode"`
			Message   string `json:"message"`
		} `json:"responseStatus"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &envelope)

	message := envelope.ResponseStatus.Message
	if message == "" {
		message = envelope.Message
	}

	return &Error{
		Status:  status,
		Code:    envelope.ResponseStatus.ErrorCode,
		Message: message,
		Body:    string(body),
	}
}

// Login exchanges EMEL credentials for a VAIMOO session: authenticate against
// EMEL, look up the EMEL user id, trade both for a single use code, then hand
// that code to VAIMOO.
func (c *Client) Login(ctx context.Context, email, password string) (*Session, error) {
	var emelAuth struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Data map[string]any `json:"data"`
	}

	err := c.request(ctx, http.MethodPost, EmelLoginURL+"emel-api/auth",
		map[string]string{"Accept": "application/json"},
		map[string]any{
			"provider": "EmailPassword",
			"credentialsEmailPassword": map[string]string{
				"email":    email,
				"password": password,
			},
		}, &emelAuth)
	if err != nil {
		return nil, emelLoginError(err)
	}
	if err := emelError(emelAuth.Error.Code, emelAuth.Error.Message); err != nil {
		return nil, err
	}

	emelToken, _ := emelAuth.Data["accessToken"].(string)
	if emelToken == "" {
		return nil, errors.New("vaimoo: EMEL login returned no access token")
	}

	var emelUser struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	err = c.request(ctx, http.MethodGet, EmelLoginURL+"emel-api/user",
		map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + emelToken,
		}, nil, &emelUser)
	if err != nil {
		return nil, err
	}
	if err := emelError(emelUser.Error.Code, emelUser.Error.Message); err != nil {
		return nil, err
	}

	// The code is bound to the whole EMEL token payload, so pass it back as it
	// arrived rather than rebuilding it field by field.
	payload := make(map[string]any, len(emelAuth.Data)+1)
	for k, v := range emelAuth.Data {
		payload[k] = v
	}
	payload["userId"] = emelUser.Data.ID

	var secureCode struct {
		Code string `json:"code"`
	}
	err = c.request(ctx, http.MethodPost, EmelLoginURL+"api/auth/code",
		map[string]string{"Accept": "application/json"},
		map[string]any{
			"payload":     payload,
			"redirectUri": RedirectURI,
		}, &secureCode)
	if err != nil {
		return nil, err
	}
	if secureCode.Code == "" {
		return nil, errors.New("vaimoo: EMEL returned no exchange code")
	}

	var login loginResponse
	if err := c.call(ctx, "auth/v2/oauth/", options{
		method: http.MethodPost,
		body:   map[string]string{"code": secureCode.Code},
	}, &login); err != nil {
		return nil, err
	}

	return newSession(login)
}

// Refresh trades a refresh token for a new session. VAIMOO rotates the refresh
// token on every call, so the result fully replaces the old session.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Session, error) {
	var login loginResponse
	err := c.call(ctx, "auth/refresh-token", options{
		method: http.MethodPost,
		headers: map[string]string{
			"RefreshToken": refreshToken,
			// Tells the backend not to try refreshing this very call.
			"no-refresh": "true",
		},
	}, &login)
	if errors.Is(err, ErrUnauthorized) {
		return nil, ErrInvalidRefreshToken
	}
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
		return nil, ErrInvalidRefreshToken
	}
	if err != nil {
		return nil, err
	}

	return newSession(login)
}

// emelError maps the EMEL error envelope, which rides along a 200 response.
func emelError(code int, message string) error {
	switch {
	case code == 0:
		return nil
	case code == 100:
		return ErrInvalidCredentials
	default:
		return fmt.Errorf("vaimoo: EMEL: %s (%d)", message, code)
	}
}

// emelLoginError recognises the validation failures EMEL reports as a 400.
func emelLoginError(err error) error {
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return err
	}
	if strings.Contains(apiErr.Body, "The field Email must") {
		return ErrInvalidEmail
	}
	return err
}

func newSession(login loginResponse) (*Session, error) {
	if login.AccessToken.Token == "" {
		return nil, errors.New("vaimoo: login returned no access token")
	}

	// The login answer names the rider userId, the refresh answer id.
	userID := login.User.UserID
	if userID == 0 {
		userID = login.User.ID
	}
	if userID == 0 {
		return nil, errors.New("vaimoo: session has no user id")
	}

	return &Session{
		AccessToken:   login.AccessToken.Token,
		RefreshToken:  login.AccessToken.RefreshToken,
		Expiry:        tokenExpiry(login.AccessToken),
		RefreshExpiry: jwtExpiry(login.AccessToken.RefreshToken),
		UserID:        userID,
	}, nil
}

// tokenExpiry reads the access token lifetime.
func tokenExpiry(tok accessToken) time.Time {
	if t := jwtExpiry(tok.Token); !t.IsZero() {
		return t
	}
	if seconds, err := strconv.Atoi(tok.ExpireSeconds); err == nil && seconds > 0 {
		return time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return time.Now().Add(accessTokenLifetime)
}

// jwtExpiry reads the exp claim of a token without verifying its signature.
func jwtExpiry(token string) time.Time {
	var claims jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil {
		return time.Time{}
	}
	if claims.ExpiresAt == nil {
		return time.Time{}
	}
	return claims.ExpiresAt.Time
}
