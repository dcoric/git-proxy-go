// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestKnownHostsVerify(t *testing.T) {
	kh := NewKnownHosts(map[string]string{"git.example.com": "SHA256:custom"})

	tests := []struct {
		name        string
		host        string
		fingerprint string
		wantErr     string
	}{
		{"github default", "github.com", defaultHostFingerprints["github.com"], ""},
		{"gitlab default", "gitlab.com", defaultHostFingerprints["gitlab.com"], ""},
		{"custom host", "git.example.com", "SHA256:custom", ""},
		{"unknown host", "evil.com", "SHA256:whatever", "unknown host"},
		{"fingerprint mismatch", "github.com", "SHA256:tampered", "possible man-in-the-middle"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := kh.verify(tc.host, tc.fingerprint)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("verify(%s) = %v, want nil", tc.host, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("verify(%s) = %v, want error containing %q", tc.host, err, tc.wantErr)
			}
		})
	}
}

// TestKnownHostsCallback exercises the ssh.HostKeyCallback path: the callback
// must compute the presented key's SHA256 fingerprint, strip the port from the
// hostname, and accept only a matching known fingerprint.
func TestKnownHostsCallback(t *testing.T) {
	signer := genSigner(t)
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())

	kh := NewKnownHosts(map[string]string{"git.example.com": fingerprint})
	cb := kh.Callback()

	if err := cb("git.example.com:22", nil, signer.PublicKey()); err != nil {
		t.Fatalf("callback with matching fingerprint and port: %v", err)
	}
	if err := cb("git.example.com", nil, signer.PublicKey()); err != nil {
		t.Fatalf("callback with matching fingerprint, no port: %v", err)
	}
	if err := cb("github.com:22", nil, signer.PublicKey()); err == nil {
		t.Fatal("callback accepted a key not matching github.com's known fingerprint")
	}
}

func TestKnownHostsCustomOverridesDefault(t *testing.T) {
	kh := NewKnownHosts(map[string]string{"github.com": "SHA256:pinned"})
	if err := kh.verify("github.com", "SHA256:pinned"); err != nil {
		t.Fatalf("custom override not applied: %v", err)
	}
	if err := kh.verify("github.com", defaultHostFingerprints["github.com"]); err == nil {
		t.Fatal("default fingerprint accepted after custom override")
	}
}
