// crypto.go — ezx.crypto: native hashing/encoding helpers (sha256sum/base64
// replacements). All implemented with the Go standard library — no external
// programs.
package script

import (
	"fmt"

	"github.com/supanadit/ezx/internal/repository"
)

// CryptoModule exposes ezx.crypto.
type CryptoModule struct{}

// NewCryptoModule returns a CryptoModule.
func NewCryptoModule() *CryptoModule {
	return &CryptoModule{}
}

// Sha256 hashes a UTF-8 string and returns the hex digest.
func (m *CryptoModule) Sha256(value string) (string, error) {
	return repository.SHA256Hex([]byte(value)), nil
}

// Sha256File hashes a file's contents and returns the hex digest.
func (m *CryptoModule) Sha256File(path string) (string, error) {
	return repository.SHA256File(path)
}

// Base64Encode encodes a UTF-8 string as standard base64.
func (m *CryptoModule) Base64Encode(value string) (string, error) {
	return repository.Base64Encode([]byte(value)), nil
}

// Base64Decode decodes standard base64 into a UTF-8 string.
func (m *CryptoModule) Base64Decode(value string) (string, error) {
	data, err := repository.Base64Decode(value)
	if err != nil {
		return "", fmt.Errorf("crypto: invalid base64: %w", err)
	}
	return string(data), nil
}

// RandomHex returns n random bytes hex-encoded (e.g. secrets, salts).
func (m *CryptoModule) RandomHex(n int) (string, error) {
	return repository.RandomHex(n)
}