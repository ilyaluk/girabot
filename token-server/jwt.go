package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// keyFunc is used to verify integrity tokens against Google keys
var keyFunc keyfunc.Keyfunc

func init() {
	var err error
	keyFunc, err = keyfunc.NewDefaultCtx(context.Background(), []string{
		"https://firebaseappcheck.googleapis.com/v1/jwks",
	})
	if err != nil {
		log.Fatal("firebasetoken: keyfunc.NewDefaultCtx:", err)
	}
}

func parseToken(token string) (*jwt.RegisteredClaims, error) {
	return parseTokenWithLeeway(token, 0)
}

func parseTokenWithLeeway(token string, leeway time.Duration) (*jwt.RegisteredClaims, error) {
	tok, err := jwt.ParseWithClaims(
		token, &jwt.RegisteredClaims{}, keyFunc.Keyfunc,
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(leeway),
	)
	if err != nil {
		return nil, fmt.Errorf("firebasetoken: can't parse token: %w", err)
	}

	if !tok.Valid {
		return nil, fmt.Errorf("firebasetoken: invalid token")
	}

	claims, ok := tok.Claims.(*jwt.RegisteredClaims)
	if !ok {
		return nil, fmt.Errorf("firebasetoken: unknown claims type")
	}

	if claims.Issuer != "https://firebaseappcheck.googleapis.com/860507348154" {
		return nil, fmt.Errorf("firebasetoken: got invalid issuer '%v'", claims.Issuer)
	}

	return claims, nil
}
