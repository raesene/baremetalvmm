package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	PrivateKeyFile = "vmm_ed25519"
	PublicKeyFile  = "vmm_ed25519.pub"
)

func EnsureKeyPair(sshDir string) error {
	privPath := filepath.Join(sshDir, PrivateKeyFile)
	pubPath := filepath.Join(sshDir, PublicKeyFile)

	if _, err := os.Stat(privPath); err == nil {
		return nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate ed25519 key: %w", err)
	}

	privBytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	if err := os.WriteFile(privPath, pem.EncodeToMemory(privBytes), 0600); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("failed to create SSH public key: %w", err)
	}

	pubKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " vmm-managed-key\n"
	if err := os.WriteFile(pubPath, []byte(pubKeyStr), 0644); err != nil {
		return fmt.Errorf("failed to write public key: %w", err)
	}

	return nil
}

func GetPublicKey(sshDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(sshDir, PublicKeyFile))
	if err != nil {
		return "", fmt.Errorf("failed to read vmm public key: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func PrivateKeyPath(sshDir string) string {
	return filepath.Join(sshDir, PrivateKeyFile)
}

// BuildAuthorizedKeys combines the vmm managed key with an optional user key.
func BuildAuthorizedKeys(sshDir, userKey string) (string, error) {
	vmmKey, err := GetPublicKey(sshDir)
	if err != nil {
		return "", err
	}

	keys := vmmKey
	if userKey = strings.TrimSpace(userKey); userKey != "" {
		keys += "\n" + userKey
	}
	return keys, nil
}
