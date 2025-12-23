package auth

import (
	"crypto/rand"
	"math/big"
)

const (
	// Alphanumeric without ambiguous characters (0O, 1lI)
	tokenChars  = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"
	tokenLength = 32

	// Uppercase alphanumeric without ambiguous characters for device ID
	deviceIDChars  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	deviceIDLength = 5
)

func GenerateToken() (string, error) {
	return generateRandomString(tokenChars, tokenLength)
}

func GenerateDeviceID() (string, error) {
	return generateRandomString(deviceIDChars, deviceIDLength)
}

func generateRandomString(chars string, length int) (string, error) {
	result := make([]byte, length)
	charsLen := big.NewInt(int64(len(chars)))

	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, charsLen)
		if err != nil {
			return "", err
		}
		result[i] = chars[n.Int64()]
	}

	return string(result), nil
}
