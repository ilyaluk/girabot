package firebasetoken

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

var (
	cachedToken   string
	cachedTokenMu sync.Mutex
)

func Get(ctx context.Context) (string, error) {
	cachedTokenMu.Lock()
	defer cachedTokenMu.Unlock()

	if isValidToken(cachedToken) {
		return cachedToken, nil
	}

	tok, err := Fetch(ctx)
	if err != nil {
		return "", err
	}

	cachedToken = tok
	return tok, nil
}

var keyFunc keyfunc.Keyfunc

func init() {
	var err error
	keyFunc, err = keyfunc.NewDefaultCtx(context.Background(), []string{"https://firebaseappcheck.googleapis.com/v1/jwks"})
	if err != nil {
		log.Fatal("firebasetoken: keyfunc.NewDefaultCtx:", err)
	}
}

func isValidToken(token string) bool {
	// Set leeway to -10 seconds to refresh token before it expires.
	tok, err := jwt.Parse(token, keyFunc.Keyfunc, jwt.WithLeeway(-10*time.Second))
	if err != nil {
		log.Println("firebasetoken: jwt.Parse:", err)
		return false
	}

	return tok.Valid
}

const tokenURL = "https://gira.rodlabs.dev/firebase-token"

func Fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "girabot (https://t.me/BetterGiraBot)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("firebasetoken: http %s", resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("firebasetoken: reading body: %w", err)
	}
	return string(bodyBytes), nil
}

type Transport struct {
	Base http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Thanks to golang.org/x/oauth2 lib for Transport implementation

	reqBodyClosed := false
	if req.Body != nil {
		defer func() {
			if !reqBodyClosed {
				req.Body.Close()
			}
		}()
	}

	token, err := Get(req.Context())
	if err != nil {
		return nil, err
	}

	req2 := cloneRequest(req) // per RoundTripper contract
	req2.Header.Set("x-firebase-token", token)

	// req.Body is assumed to be closed by the base RoundTripper.
	reqBodyClosed = true

	resp, err := t.Base.RoundTrip(req2)

	if resp.StatusCode == 401 {
		log.Printf("firebasetoken: got 401: '%s', token was '%s'", resp.Header.Get("www-authenticate"), token)
	}

	return resp, err
}

// cloneRequest returns a clone of the provided *http.Request.
// The clone is a shallow copy of the struct and its Header map.
func cloneRequest(r *http.Request) *http.Request {
	// shallow copy of the struct
	r2 := new(http.Request)
	*r2 = *r
	// deep copy of the Header
	r2.Header = make(http.Header, len(r.Header))
	for k, s := range r.Header {
		r2.Header[k] = append([]string(nil), s...)
	}
	return r2
}

func NewClient(base http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: &Transport{Base: base},
	}
}
