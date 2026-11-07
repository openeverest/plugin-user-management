package perconapg

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/xdg-go/scram"
)

const (
	scramMethod = "SCRAM-SHA-256"
	// PostgreSQL's defaults (scram_iterations, SCRAM_DEFAULT_SALT_LEN).
	scramIterations = 4096
	scramSaltLength = 16
	// Stored verifiers are recomputed every reconcile; refuse absurd costs.
	maxScramIterations = 1 << 20
)

// newVerifier hashes the password into a SCRAM-SHA-256 verifier in
// PostgreSQL's rolpassword format, as the operator does for its own users.
func newVerifier(password string) (string, error) {
	salt := make([]byte, scramSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	creds, err := scramClient(password).GetStoredCredentialsWithError(scram.KeyFactors{Salt: string(salt), Iters: scramIterations})
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("%s$%d:%s$%s:%s", scramMethod, scramIterations, b64(salt), b64(creds.StoredKey), b64(creds.ServerKey)), nil
}

// verifierMatches reports whether a rolpassword is a SCRAM-SHA-256 verifier
// of the password. Any other value, an MD5 hash or none, doesn't match.
func verifierMatches(verifier, password string) bool {
	method, rest, _ := strings.Cut(verifier, "$")
	factors, keys, _ := strings.Cut(rest, "$")
	itersText, saltText, _ := strings.Cut(factors, ":")
	storedText, serverText, _ := strings.Cut(keys, ":")
	if method != scramMethod {
		return false
	}
	iters, err := strconv.Atoi(itersText)
	if err != nil || iters < 1 || iters > maxScramIterations {
		return false
	}
	salt, err1 := base64.StdEncoding.DecodeString(saltText)
	storedKey, err2 := base64.StdEncoding.DecodeString(storedText)
	serverKey, err3 := base64.StdEncoding.DecodeString(serverText)
	if err1 != nil || err2 != nil || err3 != nil || len(salt) == 0 {
		return false
	}
	creds, err := scramClient(password).GetStoredCredentialsWithError(scram.KeyFactors{Salt: string(salt), Iters: iters})
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(creds.StoredKey, storedKey) == 1 &&
		subtle.ConstantTimeCompare(creds.ServerKey, serverKey) == 1
}

// scramClient normalizes the password with SASLprep as PostgreSQL does,
// which uses the password as is when SASLprep doesn't apply to it.
func scramClient(password string) *scram.Client {
	if c, err := scram.SHA256.NewClient("", password, ""); err == nil {
		return c
	}
	c, _ := scram.SHA256.NewClientUnprepped("", password, "")
	return c
}
