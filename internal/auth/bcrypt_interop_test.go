// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The Node build hashes passwords with bcryptjs at cost 10
// (src/db/index.ts: bcrypt.hash(password, 10)), which emits the $2b$ variant.
// This is a real bcryptjs output for the password below, captured with:
//
//	node -e "console.log(require('bcryptjs').hashSync('Sup3rSecret!', 10))"
//
// It is checked in as a fixed cross-implementation vector so the interop
// guarantee is asserted on every test run, not just on the day it was produced.
const (
	interopPassword = "Sup3rSecret!"
	bcryptjsHash    = "$2b$10$jMHZuO42X1AIyPC54qatsOlqCkyGEvOdTfL5R19a77qSucTxLGd0C"
)

// TestGoVerifiesBcryptjsHash is the direction that matters in production: a hash
// written to the user table by the Node build must verify under Go.
func TestGoVerifiesBcryptjsHash(t *testing.T) {
	if err := bcrypt.CompareHashAndPassword([]byte(bcryptjsHash), []byte(interopPassword)); err != nil {
		t.Fatalf("Go failed to verify a bcryptjs $2b$ hash: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(bcryptjsHash), []byte("wrong-password")); err == nil {
		t.Fatal("Go verified a bcryptjs hash against the wrong password")
	}
	if cost, err := bcrypt.Cost([]byte(bcryptjsHash)); err != nil || cost != 10 {
		t.Errorf("cost = %d (err %v), want 10", cost, err)
	}
}

// TestGoHashIsBcryptjsCompatible proves a Go-generated hash is in the bcrypt
// format bcryptjs accepts. The reverse direction (bcryptjs verifying this $2a$
// hash) was confirmed manually with bcryptjs.compareSync during P0-5; bcryptjs
// is not available to a Go test, so we assert the format and self-verification
// here, which is what makes the cross-check valid.
func TestGoHashIsBcryptjsCompatible(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte(interopPassword), 10)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}
	if !strings.HasPrefix(string(hash), "$2a$") && !strings.HasPrefix(string(hash), "$2b$") {
		t.Errorf("hash %q is not a $2a$/$2b$ bcrypt hash", hash)
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(interopPassword)); err != nil {
		t.Errorf("Go failed to verify its own hash: %v", err)
	}
}
