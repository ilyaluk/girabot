package main

import (
	"log"
	"net/http"
	"slices"

	"github.com/ilyaluk/girabot/internal/tokenserver"
	"golang.org/x/oauth2"
)

// fbTokenTransport is a custom http.RoundTripper
// that adds a Firebase token to request headers.
type fbTokenTransport struct {
	Base http.RoundTripper

	tokenSource oauth2.TokenSource
}

func (t *fbTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Thanks to golang.org/x/oauth2 lib for Transport implementation

	reqBodyClosed := false
	if req.Body != nil {
		defer func() {
			if !reqBodyClosed {
				req.Body.Close()
			}
		}()
	}

	tok, err := t.tokenSource.Token()
	if err != nil {
		return nil, err
	}

	token, err := tokenserver.GetEncrypted(req.Context(), tok.AccessToken)
	if err != nil {
		return nil, err
	}

	req2 := cloneRequest(req) // per RoundTripper contract
	req2.Header.Set("x-firebase-token", token)

	// req.Body is assumed to be closed by the base RoundTripper.
	reqBodyClosed = true

	resp, err := t.Base.RoundTrip(req2)
	if err != nil {
		return resp, err
	}

	if resp.StatusCode == 401 {
		log.Printf("firebasetoken: got 401: '%s', token was '%s'", resp.Header.Get("www-authenticate"), token)
	}

	return resp, nil
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
		r2.Header[k] = slices.Clone(s)
	}
	return r2
}

func newFbTokenClient(base http.RoundTripper, tokenSource oauth2.TokenSource) *http.Client {
	return &http.Client{
		Transport: &fbTokenTransport{
			Base:        base,
			tokenSource: tokenSource,
		},
	}
}
