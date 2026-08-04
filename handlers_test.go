package main

import (
	"encoding/base64"
	"errors"
	"strings"
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
			// 25 bytes, so the encoding actually pads
			name:     "padded base64",
			payload:  loginPayloadPrefix + base64.URLEncoding.EncodeToString([]byte("user@example.com:hunter22")),
			email:    "user@example.com",
			password: "hunter22",
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

func TestMakeLoginPayload(t *testing.T) {
	const email, password = "user@example.com", "hunter2"

	payload, err := makeLoginPayload(email, password)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(payload) > maxStartPayloadLen {
		t.Errorf("payload %q is %d characters long", payload, len(payload))
	}

	gotEmail, gotPassword, err := parseLoginPayload(payload)
	if err != nil {
		t.Fatalf("parsing back %q: %v", payload, err)
	}
	if gotEmail != email || gotPassword != password {
		t.Errorf("round trip gave %q/%q, want %q/%q", gotEmail, gotPassword, email, password)
	}

	if maxLoginLinkCredsLen != 47 {
		t.Errorf("expected 47 characters of credentials to fit, got %d", maxLoginLinkCredsLen)
	}

	// Credentials just at the limit fit, one character more doesn't.
	longest := strings.Repeat("p", maxLoginLinkCredsLen-len(email)-1)
	if _, err := makeLoginPayload(email, longest); err != nil {
		t.Errorf("%d characters of credentials should fit: %v", maxLoginLinkCredsLen, err)
	}
	if _, err := makeLoginPayload(email, longest+"p"); err == nil {
		t.Errorf("%d characters of credentials should not fit", maxLoginLinkCredsLen+1)
	}
}

func TestSplitCredentials(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		email    string
		password string
	}{
		{name: "empty"},
		{name: "email only", in: "user@example.com"},
		{name: "password only", in: "\nhunter2"},
		{name: "not an email", in: "my email is user@example.com"},
		{name: "email with name", in: "User <user@example.com> hunter2"},
		{name: "newline", in: "user@example.com\nhunter2", email: "user@example.com", password: "hunter2"},
		{name: "space", in: "user@example.com hunter2", email: "user@example.com", password: "hunter2"},
		{name: "surrounding space", in: "  user@example.com \n hunter2 \n", email: "user@example.com", password: "hunter2"},
		{name: "password with space", in: "user@example.com\nhunter 2", email: "user@example.com", password: "hunter 2"},
		{name: "password with colon", in: "user@example.com\nhun:ter2", email: "user@example.com", password: "hun:ter2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, password, ok := splitCredentials(tt.in)
			if ok != (tt.email != "") {
				t.Fatalf("got ok = %v for %q", ok, tt.in)
			}
			if email != tt.email || password != tt.password {
				t.Errorf("got %q/%q, want %q/%q", email, password, tt.email, tt.password)
			}
		})
	}
}

func TestCommandArgs(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "/loginlink", want: ""},
		{in: "/loginlink ", want: ""},
		{in: "/loginlink user@example.com hunter2", want: "user@example.com hunter2"},
		{in: "/loginlink@BetterGiraBot user@example.com", want: "user@example.com"},
		{in: "/loginlink\nuser@example.com\nhunter2", want: "user@example.com\nhunter2"},
	}

	for _, tt := range tests {
		if got := commandArgs(tt.in); got != tt.want {
			t.Errorf("commandArgs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
