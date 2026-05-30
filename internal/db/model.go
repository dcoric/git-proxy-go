// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package db defines the storage contract for git-proxy-go (the Store
// interface) and the domain types it persists: users, repos and pushes.
//
// It is the Go port of src/db (the Node Sink interface and types.ts). The
// concrete Postgres implementation lives in internal/db/postgres; the schema
// migrations in internal/db/migrations. JSON tags mirror the Node field names
// exactly so the pushes.data JSONB column and the migrate-data ETL (P2-4..P2-6)
// round-trip the existing documents unchanged.
package db

// User is a git-proxy account. Mirrors the User class in src/db/types.ts.
// Password is nil for OIDC-provisioned users.
type User struct {
	Username    string      `json:"username"`
	Password    *string     `json:"password"`
	GitAccount  string      `json:"gitAccount"`
	Email       string      `json:"email"`
	Admin       bool        `json:"admin"`
	OIDCID      *string     `json:"oidcId,omitempty"`
	DisplayName *string     `json:"displayName,omitempty"`
	Title       *string     `json:"title,omitempty"`
	PublicKeys  []PublicKey `json:"publicKeys,omitempty"`
}

// PublicKey is a user's registered SSH public key. Key is the authorized_keys
// form ("<algo> <base64>"); Fingerprint identifies it for management. Mirrors
// the Node PublicKeyRecord (finos/git-proxy PR #1332).
type PublicKey struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Repo is an authorised repository plus its push/authorise access lists.
// Mirrors the Repo class in src/db/types.ts; the JSON shape (project, name,
// url, users{canPush,canAuthorise}, _id) matches the Node document exactly. ID
// is the store-assigned id (Mongo _id in the Node world).
type Repo struct {
	ID      string    `json:"_id,omitempty"`
	Project string    `json:"project"`
	Name    string    `json:"name"`
	URL     string    `json:"url"`
	Users   RepoUsers `json:"users"`
}

// RepoUsers is the nested {canPush, canAuthorise} access-list shape.
type RepoUsers struct {
	CanPush      []string `json:"canPush"`
	CanAuthorise []string `json:"canAuthorise"`
}

// Push is a recorded git push and its processing outcome — the Go port of the
// Action class (src/proxy/actions/Action.ts) as stored data (no behaviour). The
// whole struct is persisted as the pushes.data JSONB column; a subset of fields
// is also promoted to indexed columns for querying (see internal/db/postgres).
type Push struct {
	ID             string       `json:"id"`
	Type           string       `json:"type"`
	Method         string       `json:"method"`
	Timestamp      int64        `json:"timestamp"`
	Project        string       `json:"project"`
	RepoName       string       `json:"repoName"`
	URL            string       `json:"url"`
	Repo           string       `json:"repo"`
	Steps          []Step       `json:"steps"`
	Error          bool         `json:"error"`
	ErrorMessage   *string      `json:"errorMessage,omitempty"`
	Blocked        bool         `json:"blocked"`
	BlockedMessage *string      `json:"blockedMessage,omitempty"`
	AllowPush      bool         `json:"allowPush"`
	Authorised     bool         `json:"authorised"`
	Canceled       bool         `json:"canceled"`
	Rejected       bool         `json:"rejected"`
	AutoApproved   bool         `json:"autoApproved"`
	AutoRejected   bool         `json:"autoRejected"`
	CommitData     []CommitData `json:"commitData,omitempty"`
	CommitFrom     string       `json:"commitFrom,omitempty"`
	CommitTo       string       `json:"commitTo,omitempty"`
	Branch         string       `json:"branch,omitempty"`
	Message        string       `json:"message,omitempty"`
	Author         string       `json:"author,omitempty"`
	User           string       `json:"user,omitempty"`
	UserEmail      string       `json:"userEmail,omitempty"`
	Attestation    *Attestation `json:"attestation,omitempty"`
	Rejection      *Rejection   `json:"rejection,omitempty"`
	LastStep       *Step        `json:"lastStep,omitempty"`
	ProxyGitPath   string       `json:"proxyGitPath,omitempty"`
	NewIdxFiles    []string     `json:"newIdxFiles,omitempty"`
}

// Step is one processor-chain step result. Mirrors StepData in
// src/proxy/actions/Step.ts.
type Step struct {
	ID             string   `json:"id"`
	StepName       string   `json:"stepName"`
	Content        any      `json:"content"`
	Error          bool     `json:"error"`
	ErrorMessage   *string  `json:"errorMessage"`
	Blocked        bool     `json:"blocked"`
	BlockedMessage *string  `json:"blockedMessage"`
	Logs           []string `json:"logs"`
}

// CommitData mirrors the CommitData type in src/proxy/processors/types.ts.
type CommitData struct {
	Tree            string `json:"tree"`
	Parent          string `json:"parent"`
	Author          string `json:"author"`
	Committer       string `json:"committer"`
	AuthorEmail     string `json:"authorEmail"`
	CommitterEmail  string `json:"committerEmail"`
	CommitTimestamp string `json:"commitTimestamp"`
	Message         string `json:"message"`
}

// Reviewer identifies who attested or rejected a push.
type Reviewer struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

// AttestationAnswer is one answered attestation question.
type AttestationAnswer struct {
	Label   string `json:"label"`
	Checked bool   `json:"checked"`
}

// Attestation is a completed review attestation (CompletedAttestation in the
// Node processors/types).
type Attestation struct {
	Reviewer  Reviewer            `json:"reviewer"`
	Timestamp string              `json:"timestamp"`
	Automated bool                `json:"automated,omitempty"`
	Answers   []AttestationAnswer `json:"answers"`
}

// Rejection records a push rejection (Rejection in the Node processors/types).
type Rejection struct {
	Reviewer  Reviewer `json:"reviewer"`
	Timestamp string   `json:"timestamp"`
	Automated bool     `json:"automated,omitempty"`
	Reason    string   `json:"reason"`
}
