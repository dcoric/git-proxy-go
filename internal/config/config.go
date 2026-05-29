// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package config loads, merges and validates git-proxy-go configuration.
//
// It mirrors the Node loader (src/config/index.ts): the embedded
// proxy.config.json provides defaults, the file named by the CONFIG_FILE
// environment variable (if present) is merged on top, the merged result is
// validated against config.schema.json (the same schema the Node build ships)
// plus the custom regex checks from validators.ts, and a small set of
// environment-variable overrides is applied. The typed shape is generated from
// the schema by quicktype; see the generated subpackage (P1-2).
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	kjson "github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/dcoric/git-proxy-go/internal/config/generated"
)

//go:embed proxy.config.json
var defaultConfigBytes []byte

//go:embed config.schema.json
var schemaBytes []byte

// schemaURL is the $id declared in config.schema.json. The compiler keys the
// resource by it and uses it as the base URI for the internal $ref pointers.
const schemaURL = "https://git-proxy.finos.org/config.schema.json"

// requiredTopLevelKeys mirrors REQUIRED_TOP_LEVEL_CONFIG_KEYS in
// src/config/index.ts. After merging, each of these must be present in the
// configuration object (the deprecated proxyUrl/sslKeyPemPath/sslCertPemPath
// fields are intentionally absent from the list).
var requiredTopLevelKeys = []string{
	"api",
	"apiAuthentication",
	"attestationConfig",
	"authentication",
	"authorisedList",
	"commitConfig",
	"configurationSources",
	"contactEmail",
	"cookieSecret",
	"csrfProtection",
	"domains",
	"plugins",
	"privateOrganizations",
	"rateLimit",
	"sessionMaxAgeHours",
	"sink",
	"tempPassword",
	"tls",
	"uiRouteAuth",
	"upstreamProxy",
	"urlShortener",
}

// Config is a loaded, merged and validated git-proxy configuration. It embeds
// the generated GitProxyConfig (the typed schema shape) and adds accessor
// helpers that mirror the Node getters (env overrides, deprecated TLS
// fallback, rate-limit translation).
type Config struct {
	generated.GitProxyConfig

	// Source is the path the user config was read from, or "" when only the
	// embedded defaults were used.
	Source string
}

// Load reads configuration the way the Node loader does: the embedded
// proxy.config.json provides defaults and the file named by CONFIG_FILE (if set
// and present) is merged on top.
func Load() (*Config, error) {
	return LoadFile(os.Getenv("CONFIG_FILE"))
}

// LoadFile merges the embedded defaults with the user config at path (path may
// be "" to use the defaults only), validates the result and returns the typed
// config. A path that does not exist is treated as "no user config", matching
// the Node behaviour of falling back to the bundled defaults.
func LoadFile(path string) (*Config, error) {
	k := koanf.New(".")
	if err := k.Load(rawbytes.Provider(defaultConfigBytes), kjson.Parser()); err != nil {
		return nil, fmt.Errorf("loading default config: %w", err)
	}

	source := ""
	if path != "" {
		switch _, statErr := os.Stat(path); {
		case statErr == nil:
			if err := k.Load(file.Provider(path), kjson.Parser()); err != nil {
				return nil, fmt.Errorf("loading config %q: %w", path, err)
			}
			source = path
		case errors.Is(statErr, os.ErrNotExist):
			// No user config; defaults stand (mirrors existsSync() === false).
		default:
			return nil, fmt.Errorf("checking config %q: %w", path, statErr)
		}
	}

	merged, err := k.Marshal(kjson.Parser())
	if err != nil {
		return nil, fmt.Errorf("marshaling merged config: %w", err)
	}
	if err := validateSchema(merged); err != nil {
		return nil, err
	}
	if err := assertRequiredTopLevel(k.Raw()); err != nil {
		return nil, err
	}

	cfg := &Config{Source: source}
	if err := k.UnmarshalWithConf("", &cfg.GitProxyConfig, koanf.UnmarshalConf{Tag: "json"}); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	applyEnvOverrides(cfg)

	if err := validateCommitConfig(&cfg.GitProxyConfig); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnvOverrides mirrors the env-var precedence in mergeConfigurations:
// GIT_PROXY_COOKIE_SECRET wins over both user and default cookieSecret.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GIT_PROXY_COOKIE_SECRET"); v != "" {
		cfg.CookieSecret = &v
	}
}

var (
	compiledSchema     *jsonschema.Schema
	compiledSchemaErr  error
	compiledSchemaOnce sync.Once
)

func schema() (*jsonschema.Schema, error) {
	compiledSchemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
		if err != nil {
			compiledSchemaErr = fmt.Errorf("parsing config schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(schemaURL, doc); err != nil {
			compiledSchemaErr = fmt.Errorf("adding config schema: %w", err)
			return
		}
		compiledSchema, compiledSchemaErr = c.Compile(schemaURL)
		if compiledSchemaErr != nil {
			compiledSchemaErr = fmt.Errorf("compiling config schema: %w", compiledSchemaErr)
		}
	})
	return compiledSchema, compiledSchemaErr
}

// validateSchema validates the merged configuration JSON against
// config.schema.json (JSON Schema draft-07).
func validateSchema(instance []byte) error {
	sch, err := schema()
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return fmt.Errorf("parsing config for validation: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return nil
}

// assertRequiredTopLevel mirrors assertHasRequiredTopLevelConfig: every key in
// requiredTopLevelKeys must be present in the merged configuration object.
func assertRequiredTopLevel(raw map[string]any) error {
	var missing []string
	for _, key := range requiredTopLevelKeys {
		if _, ok := raw[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required top-level configuration values: %s", strings.Join(missing, ", "))
	}
	return nil
}

// validateCommitConfig ports validators.ts: every configured commit-policy
// pattern must be a compilable regular expression.
//
// Parity note: Go's regexp is RE2, which rejects some constructs JavaScript
// accepts (back-references, lookaround). A pattern valid under the Node build
// may be reported invalid here; such patterns are not RE2-expressible and would
// need rewriting. This is surfaced at load time rather than silently ignored.
func validateCommitConfig(c *generated.GitProxyConfig) error {
	if c.CommitConfig == nil {
		return nil
	}
	check := func(context, pattern string) error {
		if pattern == "" {
			return nil
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid regular expression for %s: %q: %w", context, pattern, err)
		}
		return nil
	}

	cc := c.CommitConfig
	if cc.Author != nil && cc.Author.Email != nil {
		if l := cc.Author.Email.Local; l != nil && l.Block != nil {
			if err := check("commitConfig.author.email.local.block", *l.Block); err != nil {
				return err
			}
		}
		if d := cc.Author.Email.Domain; d != nil && d.Allow != nil {
			if err := check("commitConfig.author.email.domain.allow", *d.Allow); err != nil {
				return err
			}
		}
	}
	if cc.Message != nil && cc.Message.Block != nil {
		for _, p := range cc.Message.Block.Patterns {
			if err := check("commitConfig.message.block.patterns", p); err != nil {
				return err
			}
		}
	}
	if cc.Diff != nil && cc.Diff.Block != nil {
		for _, p := range cc.Diff.Block.Patterns {
			if s, ok := p.(string); ok {
				if err := check("commitConfig.diff.block.patterns", s); err != nil {
					return err
				}
			}
		}
		for _, v := range cc.Diff.Block.Providers {
			if err := check("commitConfig.diff.block.providers", v); err != nil {
				return err
			}
		}
	}
	return nil
}

// TLSEnabled reports whether HTTPS listeners should be started. Mirrors
// getTLSEnabled().
func (c *Config) TLSEnabled() bool {
	return c.TLS != nil && c.TLS.Enabled
}

// TLSKeyPath returns the private-key path, preferring tls.key and falling back
// to the deprecated sslKeyPemPath. Mirrors getTLSKeyPemPath().
func (c *Config) TLSKeyPath() string {
	if c.TLS != nil && c.TLS.Key != "" {
		return c.TLS.Key
	}
	legacy := c.SSLKeyPemPath //nolint:staticcheck // reading deprecated sslKeyPemPath is the intended fallback (mirrors getTLSKeyPemPath)
	if legacy != nil {
		return *legacy
	}
	return ""
}

// TLSCertPath returns the certificate path, preferring tls.cert and falling
// back to the deprecated sslCertPemPath. Mirrors getTLSCertPemPath().
func (c *Config) TLSCertPath() string {
	if c.TLS != nil && c.TLS.CERT != "" {
		return c.TLS.CERT
	}
	legacy := c.SSLCERTPemPath //nolint:staticcheck // reading deprecated sslCertPemPath is the intended fallback (mirrors getTLSCertPemPath)
	if legacy != nil {
		return *legacy
	}
	return ""
}

// RateLimitRPS translates the express-rate-limit window (limit requests per
// windowMs) into a token-bucket rate in requests per second. Returns 0 when no
// usable rate limit is configured.
func (c *Config) RateLimitRPS() float64 {
	if c.RateLimit == nil || c.RateLimit.WindowMS <= 0 {
		return 0
	}
	return c.RateLimit.Limit / (c.RateLimit.WindowMS / 1000.0)
}

// RateLimitBurst is the maximum burst, taken as the per-window request limit.
func (c *Config) RateLimitBurst() int {
	if c.RateLimit == nil {
		return 0
	}
	return int(c.RateLimit.Limit)
}
