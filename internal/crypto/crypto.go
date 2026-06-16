package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
)

// prefix marks values that have been encrypted by this package.
// Existing plaintext values (without the prefix) are returned as-is — transparent migration.
const prefix = "enc:"

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns plaintext unchanged when key is empty or plaintext is empty.
func Encrypt(key []byte, plaintext string) (string, error) {
	if len(key) == 0 || plaintext == "" {
		return plaintext, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts a value produced by Encrypt.
// If the value lacks the enc: prefix (legacy plaintext), it is returned as-is.
func Decrypt(key []byte, value string) (string, error) {
	if len(key) == 0 || !strings.HasPrefix(value, prefix) {
		return value, nil
	}
	data, err := base64.StdEncoding.DecodeString(value[len(prefix):])
	if err != nil {
		return value, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return value, nil
	}
	plain, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return value, nil
	}
	return string(plain), nil
}
