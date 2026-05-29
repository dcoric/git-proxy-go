// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// shutdownTimeout bounds graceful shutdown of each listener.
const shutdownTimeout = 10 * time.Second

// Listeners describes the addresses the management server binds. HTTPSAddr is
// optional; when empty only the plain-HTTP listener runs. When set, CertFile
// and KeyFile must reference a usable certificate/key pair. This mirrors the
// Node service: an HTTP listener on GIT_PROXY_UI_PORT plus an HTTPS listener on
// GIT_PROXY_HTTPS_UI_PORT when tls.enabled is set (P1-9).
type Listeners struct {
	HTTPAddr  string
	HTTPSAddr string
	CertFile  string
	KeyFile   string
}

// Servers holds bound listeners ready to serve a handler. Build it with
// NewServers so the OS-assigned addresses are known before serving — useful for
// ":0" in tests and for logging the effective port.
type Servers struct {
	handler   http.Handler
	httpLn    net.Listener
	httpsLn   net.Listener
	tlsConfig *tls.Config
}

// NewServers binds the configured listeners. When HTTPS is requested the
// certificate is loaded up front so a bad cert/key pair fails fast at start,
// mirroring the Node fs.readFileSync at boot.
func NewServers(l Listeners, h http.Handler) (*Servers, error) {
	s := &Servers{handler: h}

	httpLn, err := net.Listen("tcp", l.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("listen http %s: %w", l.HTTPAddr, err)
	}
	s.httpLn = httpLn

	if l.HTTPSAddr != "" {
		cert, err := tls.LoadX509KeyPair(l.CertFile, l.KeyFile)
		if err != nil {
			_ = httpLn.Close()
			return nil, fmt.Errorf("loading TLS keypair (cert=%q key=%q): %w", l.CertFile, l.KeyFile, err)
		}
		httpsLn, err := net.Listen("tcp", l.HTTPSAddr)
		if err != nil {
			_ = httpLn.Close()
			return nil, fmt.Errorf("listen https %s: %w", l.HTTPSAddr, err)
		}
		s.httpsLn = httpsLn
		s.tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}
	return s, nil
}

// HTTPAddr reports the effective bound plain-HTTP address.
func (s *Servers) HTTPAddr() string { return s.httpLn.Addr().String() }

// HTTPSAddr reports the effective bound HTTPS address, or "" when TLS is off.
func (s *Servers) HTTPSAddr() string {
	if s.httpsLn == nil {
		return ""
	}
	return s.httpsLn.Addr().String()
}

// Serve runs the HTTP (and HTTPS, when configured) servers until ctx is
// cancelled or a listener fails, then gracefully shuts both down. It returns
// the first non-shutdown error encountered.
func (s *Servers) Serve(ctx context.Context) error {
	httpSrv := &http.Server{Handler: s.handler, ReadHeaderTimeout: shutdownTimeout}

	// Each goroutine sends exactly one result (nil on clean shutdown).
	results := 1
	errCh := make(chan error, 2)

	go func() {
		slog.Info("management HTTP listening", "addr", s.HTTPAddr())
		errCh <- ignoreClosed(httpSrv.Serve(s.httpLn), "http server")
	}()

	var httpsSrv *http.Server
	if s.httpsLn != nil {
		results = 2
		httpsSrv = &http.Server{Handler: s.handler, TLSConfig: s.tlsConfig, ReadHeaderTimeout: shutdownTimeout}
		go func() {
			slog.Info("management HTTPS listening", "addr", s.HTTPSAddr())
			// Cert/key already live in TLSConfig, so the file args are empty.
			errCh <- ignoreClosed(httpsSrv.ServeTLS(s.httpsLn, "", ""), "https server")
		}()
	}

	var firstErr error
	select {
	case <-ctx.Done():
	case firstErr = <-errCh:
		results-- // one goroutine has already reported.
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	if httpsSrv != nil {
		_ = httpsSrv.Shutdown(shutCtx)
	}

	for i := 0; i < results; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ignoreClosed maps the benign post-Shutdown error to nil and wraps anything
// else with the listener label.
func ignoreClosed(err error, label string) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
}
