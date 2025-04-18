package firebasetoken

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

func Encrypt(integrityToken, authToken string) (string, error) {
	key, iv, err := getKeyAndIV(authToken)
	if err != nil {
		return "", fmt.Errorf("failed to get key and IV: %w", err)
	}

	plaintext := []byte(integrityToken)

	// PKCS7 padding
	blockSize := 16
	padding := blockSize - (len(plaintext) % blockSize)
	padtext := make([]byte, len(plaintext)+padding)
	copy(padtext, plaintext)
	for i := len(plaintext); i < len(padtext); i++ {
		padtext[i] = byte(padding)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	ciphertext := make([]byte, len(padtext))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padtext)

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decrypt(integrityTokenEncd, authToken string) (string, error) {
	key, iv, err := getKeyAndIV(authToken)
	if err != nil {
		return "", fmt.Errorf("failed to get key and IV: %w", err)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(integrityTokenEncd)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	if len(ciphertext) < aes.BlockSize || len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length")
	}

	plaintext := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 padding
	paddingLen := int(plaintext[len(plaintext)-1])
	if paddingLen > 16 || paddingLen < 1 {
		return "", fmt.Errorf("invalid padding")
	}

	for i := len(plaintext) - paddingLen; i < len(plaintext); i++ {
		if plaintext[i] != byte(paddingLen) {
			return "", fmt.Errorf("invalid padding")
		}
	}

	return string(plaintext[:len(plaintext)-paddingLen]), nil
}

func getKeyAndIV(authToken string) ([]byte, []byte, error) {
	tok, _, err := jwt.NewParser().ParseUnverified(authToken, jwt.MapClaims{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse auth token: %w", err)
	}

	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return nil, nil, fmt.Errorf("failed to parse auth token claims")
	}

	sub, ok := claims["sub"].(string)
	if !ok || len(sub) < 36 {
		return nil, nil, fmt.Errorf("invalid auth token: sub claim is too short")
	}

	key := strings.ReplaceAll(sub, "-", "")
	if len(key) != 32 {
		return nil, nil, fmt.Errorf("invalid auth token: sub claim is invalid")
	}

	jti, ok := claims["jti"].(string)
	if !ok || len(jti) < 16 {
		return nil, nil, fmt.Errorf("invalid auth token: jti claim is too short")
	}

	return []byte(key), []byte(jti[:16]), nil
}
