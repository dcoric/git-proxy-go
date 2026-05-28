// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package config loads and validates git-proxy-go configuration.
//
// Skeleton stub:
//   - TODO(P1-2): generate config structs from config.schema.json via quicktype.
//   - TODO(P1-3): load proxy.config.json with koanf and validate against the
//     JSON schema with santhosh-tekuri/jsonschema/v6.
package config

import "github.com/knadh/koanf/v2"

// Load loads and validates configuration. Skeleton: currently a no-op that only
// proves the koanf dependency wiring; it will load proxy.config.json unchanged
// once P1-3 lands.
func Load() error {
	_ = koanf.New(".")
	return nil
}
