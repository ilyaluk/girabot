package main

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestParseLoginPayload(t *testing.T) {
	enc := func(s string) string {
		return loginPayloadPrefix + base64.RawURLEncoding.EncodeToString([]byte(s))
	}

	tests := []struct {
		name     string
		payload  string
		email    string
		password string
		err      error
	}{
		{name: "empty", err: errNotLoginPayload},
		{name: "other payload", payload: "somestation", err: errNotLoginPayload},
		{name: "prefix only", payload: "L"},
		{name: "not base64", payload: "L!!!"},
		{name: "no separator", payload: enc("user@example.com")},
		{name: "empty password", payload: enc("user@example.com:")},
		{name: "invalid email", payload: enc("not an email:pwd")},
		{name: "email with name", payload: enc("User <user@example.com>:pwd")},
		{
			name:     "ok",
			payload:  enc("user@example.com:hunter2"),
			email:    "user@example.com",
			password: "hunter2",
		},
		{
			name:     "password with colon",
			payload:  enc("user@example.com:hun:ter2"),
			email:    "user@example.com",
			password: "hun:ter2",
		},
		{
			name:     "padded base64",
			payload:  loginPayloadPrefix + base64.URLEncoding.EncodeToString([]byte("user@example.com:hunter2")),
			email:    "user@example.com",
			password: "hunter2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, password, err := parseLoginPayload(tt.payload)

			switch {
			case tt.email == "" && err == nil:
				t.Fatalf("expected an error, got %q/%q", email, password)
			case tt.err != nil && !errors.Is(err, tt.err):
				t.Fatalf("expected error %v, got %v", tt.err, err)
			case tt.email != "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			}

			if email != tt.email || password != tt.password {
				t.Errorf("got %q/%q, want %q/%q", email, password, tt.email, tt.password)
			}
		})
	}
}

// TestLoginPayloadFits documents how many credential characters fit into the
// 64 characters of payload Telegram passes through.
func TestLoginPayloadFits(t *testing.T) {
	const maxPayload = 64

	creds := "someone.longish@example.com:correcthorse"
	payload := loginPayloadPrefix + base64.RawURLEncoding.EncodeToString([]byte(creds))
	if len(payload) > maxPayload {
		t.Fatalf("payload of %d chars for %d chars of credentials does not fit", len(payload), len(creds))
	}

	// The longest "email:password" that still fits.
	fits := 0
	for n := 1; n <= 100; n++ {
		if len(loginPayloadPrefix)+base64.RawURLEncoding.EncodedLen(n) <= maxPayload {
			fits = n
		}
	}
	if fits != 47 {
		t.Errorf("expected 47 credential characters to fit, got %d", fits)
	}
}
