package vaimoo

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{in: "2027-08-26T19:52:00.0000Z", want: time.Date(2027, 8, 26, 19, 52, 0, 0, time.UTC)},
		{in: "2026-09-15T21:20:22.308", want: time.Date(2026, 9, 15, 21, 20, 22, 308000000, time.UTC)},
		{in: "2026-09-15T21:20:22", want: time.Date(2026, 9, 15, 21, 20, 22, 0, time.UTC)},
		{in: "", wantErr: true},
		{in: "whenever", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTime(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got %v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocalTimestampRoundTrips(t *testing.T) {
	when := time.Date(2026, 9, 15, 21, 20, 22, 308000000, time.UTC)

	rendered := LocalTimestamp(when)
	if rendered != "2026-09-15T21:20:22.308" {
		t.Fatalf("LocalTimestamp() = %q", rendered)
	}

	got, err := ParseTime(rendered)
	if err != nil || !got.Equal(when) {
		t.Errorf("parsing back gave %v, %v", got, err)
	}
}

// makeJWT builds an unsigned token carrying just an expiry.
func makeJWT(exp time.Time) string {
	header, _ := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{"sub": "42", "exp": exp.Unix()})

	encode := base64.RawURLEncoding.EncodeToString
	return encode(header) + "." + encode(payload) + ".signature"
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute).Truncate(time.Second)

	// The token itself is the authority on its own lifetime.
	got := tokenExpiry(accessToken{Token: makeJWT(exp), ExpireSeconds: "3600"})
	if !got.Equal(exp) {
		t.Errorf("got %v, want the JWT expiry %v", got, exp)
	}

	// A token that says nothing falls back to what the server sent.
	got = tokenExpiry(accessToken{Token: "not-a-jwt", ExpireSeconds: "60"})
	if delta := time.Until(got); delta < 55*time.Second || delta > 60*time.Second {
		t.Errorf("got %v, want about a minute out", delta)
	}

	// With neither, the documented five minutes stands in.
	got = tokenExpiry(accessToken{Token: "not-a-jwt"})
	if delta := time.Until(got); delta < 4*time.Minute || delta > 5*time.Minute {
		t.Errorf("got %v, want about five minutes out", delta)
	}
}

func TestNewSession(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	refreshExp := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)

	// The login answer names the rider userId.
	session, err := newSession(loginResponse{
		AccessToken: accessToken{Token: makeJWT(exp), RefreshToken: makeJWT(refreshExp)},
		User:        User{UserID: 658933},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.UserID != 658933 {
		t.Errorf("UserID = %d", session.UserID)
	}
	if !session.Expiry.Equal(exp) || !session.RefreshExpiry.Equal(refreshExp) {
		t.Errorf("expiries are %v / %v", session.Expiry, session.RefreshExpiry)
	}
	if !session.Valid() || !session.RefreshValid() {
		t.Error("a fresh session should be valid")
	}

	// The refresh answer names the same field id.
	session, err = newSession(loginResponse{
		AccessToken: accessToken{Token: makeJWT(exp), RefreshToken: makeJWT(refreshExp)},
		User:        User{ID: 658933},
	})
	if err != nil || session.UserID != 658933 {
		t.Errorf("got %+v, %v", session, err)
	}

	if _, err := newSession(loginResponse{User: User{ID: 1}}); err == nil {
		t.Error("a response with no access token should fail")
	}
	if _, err := newSession(loginResponse{AccessToken: accessToken{Token: makeJWT(exp)}}); err == nil {
		t.Error("a response with no user id should fail")
	}
}

func TestSessionValidity(t *testing.T) {
	var nilSession *Session
	if nilSession.Valid() || nilSession.RefreshValid() {
		t.Error("a missing session is never valid")
	}

	// The margin keeps a token that dies mid-flight from being handed out.
	almostGone := &Session{AccessToken: "t", Expiry: time.Now().Add(5 * time.Second)}
	if almostGone.Valid() {
		t.Error("a token expiring within the margin should count as expired")
	}

	// A refresh token whose expiry could not be read is still worth trying.
	unknown := &Session{RefreshToken: "t"}
	if !unknown.RefreshValid() {
		t.Error("an unknown refresh expiry should not rule the token out")
	}
}
