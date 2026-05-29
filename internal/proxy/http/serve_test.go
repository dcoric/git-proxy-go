// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// genSelfSigned writes a self-signed cert/key pair valid for 127.0.0.1 to a
// temp dir and returns (certFile, keyFile, certPEM).
func genSelfSigned(t *testing.T) (string, string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "git-proxy-go-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile, certPEM
}

func TestServeHTTP(t *testing.T) {
	servers, err := NewServers(Listeners{HTTPAddr: "127.0.0.1:0"}, NewRouter(Options{}))
	if err != nil {
		t.Fatalf("NewServers: %v", err)
	}
	if servers.HTTPSAddr() != "" {
		t.Errorf("HTTPSAddr() = %q, want empty (TLS off)", servers.HTTPSAddr())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- servers.Serve(ctx) }()

	resp, err := http.Get("http://" + servers.HTTPAddr() + "/api/v1/healthcheck")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve returned %v, want nil after graceful shutdown", err)
	}
}

func TestServeHTTPS(t *testing.T) {
	certFile, keyFile, certPEM := genSelfSigned(t)

	servers, err := NewServers(Listeners{
		HTTPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
		CertFile:  certFile,
		KeyFile:   keyFile,
	}, NewRouter(Options{}))
	if err != nil {
		t.Fatalf("NewServers: %v", err)
	}
	if servers.HTTPSAddr() == "" {
		t.Fatal("HTTPSAddr() empty, want a bound address")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- servers.Serve(ctx) }()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to add test cert to pool")
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		Timeout:   5 * time.Second,
	}
	resp, err := client.Get("https://" + servers.HTTPSAddr() + "/api/v1/healthcheck")
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

func TestNewServersBadCert(t *testing.T) {
	_, err := NewServers(Listeners{
		HTTPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
		CertFile:  filepath.Join(t.TempDir(), "missing-cert.pem"),
		KeyFile:   filepath.Join(t.TempDir(), "missing-key.pem"),
	}, NewRouter(Options{}))
	if err == nil {
		t.Fatal("NewServers: expected error for missing cert/key, got nil")
	}
}
