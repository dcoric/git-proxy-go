// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import "context"

// rawBodyKey is the private context key under which the git transport proxy
// stashes the buffered pack body for pack POSTs (the Go equivalent of the Node
// extractRawBody middleware setting req.bodyRaw). parsePush (#40) reads it via
// RawBody; the body is also forwarded upstream unchanged.
type rawBodyKey struct{}

// WithRawBody returns a context carrying the buffered request body.
func WithRawBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, rawBodyKey{}, body)
}

// RawBody returns the buffered request body stashed by WithRawBody, if any.
func RawBody(ctx context.Context) ([]byte, bool) {
	b, ok := ctx.Value(rawBodyKey{}).([]byte)
	return b, ok
}
