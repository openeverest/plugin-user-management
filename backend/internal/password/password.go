// Package password generates and validates database user passwords.
package password

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	generatedLength = 24
	// MinLength and MaxLength bound user-supplied passwords.
	MinLength = 8
	MaxLength = 128
	// Letters and digits only, so generated passwords need no escaping in
	// connection URIs, shells or YAML.
	alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
)

// Generate returns a random password.
func Generate() (string, error) {
	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, generatedLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

// Validate checks a user-supplied password.
func Validate(password string) error {
	if len(password) < MinLength || len(password) > MaxLength {
		return fmt.Errorf("password must be %d to %d characters long", MinLength, MaxLength)
	}
	for _, r := range password {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("password must not contain control characters")
		}
	}
	return nil
}
