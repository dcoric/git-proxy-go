// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"fmt"
	"net"

	"golang.org/x/crypto/ssh"
)

// defaultHostFingerprints are the SHA256 host-key fingerprints of common git
// hosts, so the proxy can verify upstream connections out of the box (P5-3,
// ported from knownHosts.ts DEFAULT_KNOWN_HOSTS). Verify against the providers'
// published fingerprints periodically.
var defaultHostFingerprints = map[string]string{
	"github.com": "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU",
	"gitlab.com": "SHA256:eUXGGm1YGsMAS7vkcx6JOJdOGHPem5gQp4taiCfCLB8",
}

// KnownHosts verifies upstream SSH host keys against expected SHA256
// fingerprints (the github.com/gitlab.com defaults plus any overrides).
type KnownHosts struct {
	fingerprints map[string]string
}

// NewKnownHosts builds the known-hosts set, merging custom hostname→fingerprint
// entries over the defaults.
func NewKnownHosts(custom map[string]string) *KnownHosts {
	fps := make(map[string]string, len(defaultHostFingerprints)+len(custom))
	for h, fp := range defaultHostFingerprints {
		fps[h] = fp
	}
	for h, fp := range custom {
		fps[h] = fp
	}
	return &KnownHosts{fingerprints: fps}
}

// Callback returns an ssh.HostKeyCallback that verifies the upstream host key's
// SHA256 fingerprint against the known set (fail-closed on unknown host or
// mismatch).
func (k *KnownHosts) Callback() ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		host := hostWithoutPort(hostname)
		return k.verify(host, ssh.FingerprintSHA256(key))
	}
}

// verify checks a host's presented fingerprint against the known set. Port of
// verifyHostKey.
func (k *KnownHosts) verify(host, fingerprint string) error {
	expected, ok := k.fingerprints[host]
	if !ok {
		return fmt.Errorf("host key verification failed: unknown host %q (add its SHA256 host-key fingerprint to the known hosts)", host)
	}
	if fingerprint != expected {
		return fmt.Errorf("host key verification failed for %q: expected %s, got %s (possible man-in-the-middle)", host, expected, fingerprint)
	}
	return nil
}

func hostWithoutPort(hostname string) string {
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		return h
	}
	return hostname
}
