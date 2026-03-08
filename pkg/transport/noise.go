package transport

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flynn/noise"
)

const (
	noisePrivateKeySize = 32
	noisePublicKeySize  = 32
)

var (
	noiseBasepoint = []byte{9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	noiseSuite     = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
)

// GenerateKeypair creates a new Noise Protocol static keypair.
// Returns (privateKey, publicKey) as 32-byte slices.
func GenerateKeypair() (priv, pub []byte) {
	keypair, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, nil
	}
	priv = append([]byte(nil), keypair.Private...)
	pub = append([]byte(nil), keypair.Public...)
	return priv, pub
}

// DerivePublicKey derives a Noise static public key from a private key.
func DerivePublicKey(privateKey []byte) ([]byte, error) {
	if len(privateKey) != noisePrivateKeySize {
		return nil, fmt.Errorf("invalid noise private key length: %d", len(privateKey))
	}
	publicKey, err := noise.DH25519.DH(privateKey, noiseBasepoint)
	if err != nil {
		return nil, fmt.Errorf("derive noise public key: %w", err)
	}
	if len(publicKey) != noisePublicKeySize {
		return nil, fmt.Errorf("derived invalid noise public key length: %d", len(publicKey))
	}
	return append([]byte(nil), publicKey...), nil
}

// LoadOrGenerateKey loads a keypair from a file, or generates one if it doesn't exist.
// File format: 64 hex chars (32-byte private key). Public key is derived.
func LoadOrGenerateKey(path string) (priv, pub []byte, err error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, errors.New("noise key path is empty")
	}
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		raw := strings.TrimSpace(string(data))
		decoded, decodeErr := hex.DecodeString(raw)
		if decodeErr != nil {
			return nil, nil, fmt.Errorf("decode noise private key from %s: %w", path, decodeErr)
		}
		if len(decoded) != noisePrivateKeySize {
			return nil, nil, fmt.Errorf("invalid noise private key length in %s: %d", path, len(decoded))
		}
		derivedPub, deriveErr := DerivePublicKey(decoded)
		if deriveErr != nil {
			return nil, nil, deriveErr
		}
		return append([]byte(nil), decoded...), derivedPub, nil
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read noise key file %s: %w", path, readErr)
	}

	newPriv, newPub := GenerateKeypair()
	if len(newPriv) != noisePrivateKeySize || len(newPub) != noisePublicKeySize {
		return nil, nil, errors.New("failed to generate noise keypair")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create noise key directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(newPriv)), 0o600); err != nil {
		return nil, nil, fmt.Errorf("write noise key file %s: %w", path, err)
	}
	return newPriv, newPub, nil
}
