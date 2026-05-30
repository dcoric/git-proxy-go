// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// EnsureHostKey returns the proxy's SSH host key signer, loading the private key
// at path or generating a new Ed25519 key (and persisting it) when absent. The
// host key identifies the proxy to connecting clients (like a TLS certificate);
// it is not used to authenticate to upstream git servers. Port of
// hostKeyManager.ensureHostKey — generation is in-process rather than shelling
// out to ssh-keygen.
func EnsureHostKey(path string) (ssh.Signer, error) {
	if b, err := os.ReadFile(path); err == nil {
		signer, perr := ssh.ParsePrivateKey(b)
		if perr != nil {
			return nil, fmt.Errorf("parsing SSH host key %s: %w", path, perr)
		}
		return signer, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading SSH host key %s: %w", path, err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating SSH host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "git-proxy-host-key")
	if err != nil {
		return nil, fmt.Errorf("marshalling SSH host key: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating SSH host key dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, fmt.Errorf("writing SSH host key %s: %w", path, err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("building SSH host key signer: %w", err)
	}
	return signer, nil
}
