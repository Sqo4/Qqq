// Package signing signs and verifies client update manifests with Ed25519.
// The private key lives only in CI secrets; the public key is compiled into
// the client, so an update is installed only if it was signed by us,
// no matter where the manifest was downloaded from.
package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Manifest describes one installer for one platform.
type Manifest struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"` // base64 Ed25519 over Payload()
}

// Payload is the exact byte string that gets signed.
func (m Manifest) Payload() []byte {
	return []byte(strings.Join([]string{"uley-update-v1", m.Version, m.OS, m.Arch, m.URL, strings.ToLower(m.SHA256)}, "\n"))
}

func Sign(m Manifest, privB64 string) (Manifest, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return m, errors.New("signing: bad private key")
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(raw), m.Payload()))
	return m, nil
}

func Verify(m Manifest, pubB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("signing: bad public key")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return fmt.Errorf("signing: bad signature encoding: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), m.Payload(), sig) {
		return errors.New("signing: signature does not match")
	}
	return nil
}

// GenerateKey returns a new key pair, base64 encoded.
func GenerateKey() (pubB64, privB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(priv), nil
}
