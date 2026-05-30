// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureHostKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "proxy_host_key")

	// First call generates and persists the key.
	signer, err := EnsureHostKey(path)
	if err != nil {
		t.Fatalf("EnsureHostKey (generate): %v", err)
	}
	if signer == nil {
		t.Fatal("nil signer")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("host key not persisted: %v", err)
	}
	firstFP := signer.PublicKey().Marshal()

	// Second call loads the existing key (same public key).
	again, err := EnsureHostKey(path)
	if err != nil {
		t.Fatalf("EnsureHostKey (load): %v", err)
	}
	if string(again.PublicKey().Marshal()) != string(firstFP) {
		t.Error("reloaded host key differs from the generated one")
	}
}

func TestEnsureHostKeyInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad_key")
	if err := os.WriteFile(path, []byte("not a private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureHostKey(path); err == nil {
		t.Error("expected an error for an unparseable host key")
	}
}
