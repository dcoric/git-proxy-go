// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost matches the Node build, which hashes with bcryptjs at cost 10
// (src/db/index.ts). The interop is proven in bcrypt_interop_test.go.
const bcryptCost = 10

// HashPassword returns a bcrypt hash of the password, compatible with the
// bcryptjs hashes in the existing user table.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword reports whether password matches the stored bcrypt hash. It is
// safe to call with hashes produced by either Go's bcrypt or Node's bcryptjs.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
