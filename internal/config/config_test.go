// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

// TestLoadDefaults proves the embedded proxy.config.json loads unchanged and
// validates — the core of the P1 exit gate.
func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFile("")
	if err != nil {
		t.Fatalf("LoadFile(\"\"): %v", err)
	}
	if cfg.Source != "" {
		t.Errorf("Source = %q, want empty", cfg.Source)
	}
	if cfg.CookieSecret == nil || *cfg.CookieSecret != "cookie secret" {
		t.Errorf("CookieSecret = %v, want \"cookie secret\"", cfg.CookieSecret)
	}
	if cfg.SessionMaxAgeHours == nil || *cfg.SessionMaxAgeHours != 12 {
		t.Errorf("SessionMaxAgeHours = %v, want 12", cfg.SessionMaxAgeHours)
	}
	if cfg.CSRFProtection == nil || !*cfg.CSRFProtection {
		t.Errorf("CSRFProtection = %v, want true", cfg.CSRFProtection)
	}
	if cfg.RateLimit == nil || cfg.RateLimit.Limit != 1000 || cfg.RateLimit.WindowMS != 60000 {
		t.Errorf("RateLimit = %+v, want limit 1000 / windowMs 60000", cfg.RateLimit)
	}
	if got := len(cfg.Sink); got != 2 {
		t.Errorf("len(Sink) = %d, want 2", got)
	}
	if got := len(cfg.Authentication); got != 3 {
		t.Errorf("len(Authentication) = %d, want 3", got)
	}
	if len(cfg.AuthorisedList) != 1 || cfg.AuthorisedList[0].Project != "finos" {
		t.Errorf("AuthorisedList = %+v, want one finos entry", cfg.AuthorisedList)
	}
	if cfg.TLSEnabled() {
		t.Errorf("TLSEnabled() = true, want false")
	}
	if got := cfg.TLSKeyPath(); got != "certs/key.pem" {
		t.Errorf("TLSKeyPath() = %q, want certs/key.pem", got)
	}
	if got := cfg.TLSCertPath(); got != "certs/cert.pem" {
		t.Errorf("TLSCertPath() = %q, want certs/cert.pem", got)
	}
}

// TestRateLimitTranslation checks the express-rate-limit window→RPS mapping.
func TestRateLimitTranslation(t *testing.T) {
	cfg, err := LoadFile("")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	// 1000 requests / 60s ≈ 16.67 rps; burst == limit.
	if got := cfg.RateLimitRPS(); got < 16.6 || got > 16.7 {
		t.Errorf("RateLimitRPS() = %v, want ~16.67", got)
	}
	if got := cfg.RateLimitBurst(); got != 1000 {
		t.Errorf("RateLimitBurst() = %d, want 1000", got)
	}
}

// TestLoadMergesUserFile proves the user file overrides defaults key-by-key
// (deep merge) while untouched keys keep their default values.
func TestLoadMergesUserFile(t *testing.T) {
	path := writeTemp(t, `{
		"csrfProtection": false,
		"sessionMaxAgeHours": 1,
		"rateLimit": { "windowMs": 1000, "limit": 5 },
		"tls": { "enabled": true, "key": "/tmp/k.pem", "cert": "/tmp/c.pem" }
	}`)

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Source != path {
		t.Errorf("Source = %q, want %q", cfg.Source, path)
	}
	if cfg.CSRFProtection == nil || *cfg.CSRFProtection {
		t.Errorf("CSRFProtection = %v, want false (overridden)", cfg.CSRFProtection)
	}
	if cfg.SessionMaxAgeHours == nil || *cfg.SessionMaxAgeHours != 1 {
		t.Errorf("SessionMaxAgeHours = %v, want 1 (overridden)", cfg.SessionMaxAgeHours)
	}
	if cfg.RateLimit == nil || cfg.RateLimit.Limit != 5 || cfg.RateLimit.WindowMS != 1000 {
		t.Errorf("RateLimit = %+v, want limit 5 / windowMs 1000", cfg.RateLimit)
	}
	// Untouched default survives the merge.
	if cfg.CookieSecret == nil || *cfg.CookieSecret != "cookie secret" {
		t.Errorf("CookieSecret = %v, want default to survive merge", cfg.CookieSecret)
	}
	if !cfg.TLSEnabled() {
		t.Errorf("TLSEnabled() = false, want true")
	}
	if got := cfg.TLSKeyPath(); got != "/tmp/k.pem" {
		t.Errorf("TLSKeyPath() = %q, want /tmp/k.pem", got)
	}
}

// TestEnvOverrideCookieSecret mirrors the GIT_PROXY_COOKIE_SECRET precedence.
func TestEnvOverrideCookieSecret(t *testing.T) {
	t.Setenv("GIT_PROXY_COOKIE_SECRET", "from-env")
	cfg, err := LoadFile("")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.CookieSecret == nil || *cfg.CookieSecret != "from-env" {
		t.Errorf("CookieSecret = %v, want from-env", cfg.CookieSecret)
	}
}

// TestTLSLegacyFallback proves the deprecated sslKeyPemPath/sslCertPemPath are
// used when tls.key/tls.cert are blank (getTLSKeyPemPath/getTLSCertPemPath).
func TestTLSLegacyFallback(t *testing.T) {
	path := writeTemp(t, `{
		"tls": { "enabled": true, "key": "", "cert": "" },
		"sslKeyPemPath": "/legacy/key.pem",
		"sslCertPemPath": "/legacy/cert.pem"
	}`)

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := cfg.TLSKeyPath(); got != "/legacy/key.pem" {
		t.Errorf("TLSKeyPath() = %q, want /legacy/key.pem", got)
	}
	if got := cfg.TLSCertPath(); got != "/legacy/cert.pem" {
		t.Errorf("TLSCertPath() = %q, want /legacy/cert.pem", got)
	}
}

// TestMissingFileFallsBackToDefaults: a CONFIG_FILE that doesn't exist is not
// an error — the defaults stand (existsSync === false in Node).
func TestMissingFileFallsBackToDefaults(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadFile(missing): %v", err)
	}
	if cfg.Source != "" {
		t.Errorf("Source = %q, want empty for a missing file", cfg.Source)
	}
}

func TestSchemaValidationRejectsUnknownTopLevelKey(t *testing.T) {
	path := writeTemp(t, `{ "totallyUnknownKey": true }`)
	if _, err := LoadFile(path); err == nil {
		t.Fatal("LoadFile: expected schema error for unknown top-level key, got nil")
	}
}

func TestSchemaValidationRejectsWrongType(t *testing.T) {
	path := writeTemp(t, `{ "rateLimit": { "windowMs": "not-a-number", "limit": 5 } }`)
	if _, err := LoadFile(path); err == nil {
		t.Fatal("LoadFile: expected schema error for non-numeric windowMs, got nil")
	}
}

func TestInvalidCommitRegexRejected(t *testing.T) {
	path := writeTemp(t, `{
		"commitConfig": { "diff": { "block": { "patterns": ["(unterminated"] } } }
	}`)
	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("LoadFile: expected invalid-regex error, got nil")
	}
}

func TestAssertRequiredTopLevel(t *testing.T) {
	// A map missing keys must be reported.
	if err := assertRequiredTopLevel(map[string]any{"api": map[string]any{}}); err == nil {
		t.Fatal("assertRequiredTopLevel: expected error for sparse map, got nil")
	}
	// A map with every required key present must pass.
	full := make(map[string]any, len(requiredTopLevelKeys))
	for _, k := range requiredTopLevelKeys {
		full[k] = true
	}
	if err := assertRequiredTopLevel(full); err != nil {
		t.Errorf("assertRequiredTopLevel(full) = %v, want nil", err)
	}
}
