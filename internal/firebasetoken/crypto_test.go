package firebasetoken

import (
	"strings"
	"testing"

	"github.com/golang-jwt/jwt"
)

func TestEncrypt(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "45e33173-2943-47ae-92de-59afbcab4c4c",
		"jti": "3ebb9117-7150-4547-8cca-f51fd6e55f46",
	})

	authToken, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("failed to create test token: %v", err)
	}

	t.Log("authToken: ", authToken)

	if authToken == "" {
		t.Fatal("failed to generate token")
	}

	intgr := strings.Repeat("e", 960)

	enc, err := Encrypt(intgr, authToken)
	if err != nil {
		t.Errorf("failed to encrypt: %v", err)
	}

	expected := "UKehtAXLfi2QSNQgLeXybK1OpQGF0uRcSO4oUz+1QbHA+llDGHR8CRX2NZ5ekkszvLAp3cPoQlPL/En2zWBr8FbMa1MMyaB6N33f1FeFx7nClPgbeSagld/EKuNg8RQE4Pwf6ovTbyj9jcIOZXrQTvhxvb4L5rFr4lQ8FsTHBk4aZqggbVjdbDTV6JE/U+CASj8tky77EPTctJilaSFpq2mQtcYRd2VwlPoMLTP/m23AyHwE+ZMwMy2EvTjz2CWSVzL6K0PkeNCqRVFPZBBRU4XcWq3DIiNj0FpMLjOtDJiwmBBh8JGGULd37aQWi4S8m+edwavNeGx/XDv6U9r5v+g9cJDr0plDAEGbuPAB7N8HxCU46Uv58Cdbj+Q3X6DZ/fpUpge4ITgKbScKi94Hyxoxg+ZHJD8frGWmfyJM0CKWInNFZiM/RVn6nivw5W0V/zMKshMtTKibNUu6a5qjvQ5Ku7d/UNkhgAJN11BE/95lXubOLZbWsMB+wKBtohLJoaJ80DAE8goia9qO0oxn1D6JRozg/zLzXFxcsVGdJac67LN+ZXVChWLOFFPJugBqFyo0TOPoTkvFbJNwAvQBwVdWhmGbAwHydAX/EOlADtRiLjGzseRf8U1Hq+z7vPTi0jCqMx4+iFbN8vaX1NWOed0dZcQ5hhVGDKhM+DgEyz0ZLIKc8SVvgcoBGBETdLBF6inPfFWu+kA65Q3tcysYAB2zSOwMR5sQhM0dBSP/bZt5vCILbCG9K+wp3P2Sxg2NmlyQa5+GgWZrD43xuLcQeb5RALn7gL2dkGYASuhXpYogMse0Ka8Gy9Ma48CDw/doQYW6MMNnTY+SM8VEJqxjZbEz60SbVTtjOnqgdLi6/ydMfw0jXgNv98asZ0S7RNJNjfbnpJCto3s7spSJ86846GNGvKAOdr/Buvh9G6RmDJ2Jz8JH9ZhWq6OolHtfcXmc4acgOTZZkAnAzMrf2PEZVUh7zvPLKJ5OhNEY6k/j+Y5OrL09vzSUAUGTyNn/aw9vhBU6j8JZ4KXMky9ts+IMPXZZloq8LmVEPWpZxXeTitQiS55LD7I4oeHZX3cJdGcz5Ul4m1jLzemSX5+nuc/9wR93gAy9DYdtNdpS9A1/KVCxgEAPo8dgq6OCGezlGyugPfFGTrfOGq3q2zYDhqKRhVGOeEcURY2Pe5P5klcHyu32DX0u6KgnhtjFpdBYOAu9/ucSBygrzGvRV28V6357P/sNN5zmDOamX4nizk+okWY8iO8+Fs5+ewbryKUbgvKkSkxX/NFBDtC4t2LHUVAK9g=="
	if enc != expected {
		t.Errorf("encrypted token does not match expected value: got %s, want %s", enc, expected)
	}

	dec, err := decrypt(enc, authToken)
	if err != nil {
		t.Errorf("failed to decrypt: %v", err)
	}

	if dec != intgr {
		t.Errorf("decrypted token does not match original value: got %s, want %s", dec, intgr)
	}
}
