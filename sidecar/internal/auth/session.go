// Package auth owns the process-local session credential format.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

const (
	HeaderName = "X-VibeTable-Session"
	secretSize = 32
)

var ErrInvalidSecret = errors.New("session secret must encode exactly 256 bits")

type Secret struct {
	value [secretSize]byte
}

func Parse(encoded string) (Secret, error) {
	decoded, err := decode(encoded)
	if err != nil || len(decoded) != secretSize {
		return Secret{}, ErrInvalidSecret
	}

	var result Secret
	copy(result.value[:], decoded)
	return result, nil
}

func Generate() (Secret, string, error) {
	var result Secret
	if _, err := rand.Read(result.value[:]); err != nil {
		return Secret{}, "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(result.value[:])
	return result, encoded, nil
}

func (secret Secret) Matches(candidate string) bool {
	decoded, err := decode(candidate)
	if err != nil || len(decoded) != secretSize {
		return false
	}
	return subtle.ConstantTimeCompare(secret.value[:], decoded) == 1
}

func (secret Secret) IsZero() bool {
	var zero [secretSize]byte
	return subtle.ConstantTimeCompare(secret.value[:], zero[:]) == 1
}

func decode(encoded string) ([]byte, error) {
	if len(encoded) == hex.EncodedLen(secretSize) {
		return hex.DecodeString(encoded)
	}
	return base64.RawURLEncoding.DecodeString(encoded)
}
