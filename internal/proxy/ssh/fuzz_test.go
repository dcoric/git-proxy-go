// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh/agent"
)

// FuzzReadRefAdvertisement fuzzes the hand-rolled pkt-line parser that relays an
// upstream git server's ref advertisement to the client (P5-5 / #59). The
// upstream is untrusted input, so the parser must never panic or loop forever,
// and any advertisement it accepts must be a prefix of the input ending in a
// flush packet.
func FuzzReadRefAdvertisement(f *testing.F) {
	f.Add(refAdvertisement())
	f.Add([]byte("0000"))                // bare flush
	f.Add([]byte(""))                    // empty
	f.Add([]byte("0001"))                // reserved length < 4
	f.Add([]byte("0003abc"))             // length 3, malformed
	f.Add([]byte("zzzz"))                // non-hex length
	f.Add([]byte("ffff"))                // huge length, truncated payload
	f.Add([]byte("0009abcde0000"))       // one line then flush
	f.Add([]byte("0008abc\n0008def\n"))  // two lines, no flush (EOF)
	f.Add(append([]byte("0005a"), 0xff)) // tiny line + trailing byte

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := readRefAdvertisement(bytes.NewReader(data))
		if err != nil {
			return // a parse error is an acceptable outcome
		}
		if !bytes.HasSuffix(got, []byte("0000")) {
			t.Errorf("accepted advertisement without trailing flush packet: %q", got)
		}
		if len(got) > len(data) {
			t.Errorf("returned %d bytes from a %d-byte input", len(got), len(data))
		}
		if !bytes.Equal(got, data[:len(got)]) {
			t.Errorf("returned bytes are not a prefix of the input")
		}
	})
}

// FuzzParseGitCommand fuzzes the parser for the untrusted client exec command.
// It must never panic, and every command it accepts must satisfy the
// path-traversal-safety invariants (no "..", no "//", a host/org/repo.git shape).
func FuzzParseGitCommand(f *testing.F) {
	f.Add("git-upload-pack 'github.com/org/repo.git'")
	f.Add("git-receive-pack '/a/b/c.git'")
	f.Add("git-upload-pack '../../etc/shadow.git'")
	f.Add("git-upload-pack 'a/b/c.git'; rm -rf /")
	f.Add("git-shell -c whatever")
	f.Add("")
	f.Add("git-upload-pack ''")

	f.Fuzz(func(t *testing.T, cmd string) {
		op, repo, err := parseGitCommand(cmd)
		if err != nil {
			return // rejected — fine
		}
		if op != "upload-pack" && op != "receive-pack" {
			t.Errorf("accepted unexpected operation %q from %q", op, cmd)
		}
		if strings.Contains(repo, "..") || strings.Contains(repo, "//") {
			t.Errorf("accepted path-traversal repo %q from %q", repo, cmd)
		}
		if !strings.HasSuffix(repo, ".git") {
			t.Errorf("accepted non-.git repo %q from %q", repo, cmd)
		}
		if len(strings.Split(repo, "/")) < 3 {
			t.Errorf("accepted repo with fewer than 3 segments %q from %q", repo, cmd)
		}
	})
}

// fuzzAgentConn is an in-memory ssh-agent connection that swallows the client's
// request and serves fuzzed response bytes, so a single FuzzAgentResponse
// iteration is deterministic and goroutine-free.
type fuzzAgentConn struct{ resp *bytes.Reader }

func (c *fuzzAgentConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *fuzzAgentConn) Read(p []byte) (int, error)  { return c.resp.Read(p) }

// FuzzAgentResponse fuzzes the agent-protocol round-trip from the proxy's
// consuming side: a (potentially hostile) forwarded agent's reply to the
// List/Signers calls the SSH handler makes via openForwardedAgent. Go delegates
// the agent wire protocol to x/crypto/ssh/agent (unlike Node's hand-rolled
// AgentProxy), so this confirms the proxy's consumption stays panic-free against
// arbitrary agent responses.
func FuzzAgentResponse(f *testing.F) {
	f.Add([]byte{0, 0, 0, 5, 12, 0, 0, 0, 0}) // SSH_AGENT_IDENTITIES_ANSWER, 0 keys
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 1, 99})         // unknown message type
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}) // oversized length prefix

	f.Fuzz(func(t *testing.T, resp []byte) {
		ac := agent.NewClient(&fuzzAgentConn{resp: bytes.NewReader(resp)})
		// Must not panic regardless of what the agent returns.
		if keys, err := ac.List(); err == nil {
			for _, k := range keys {
				_ = k.String()
			}
		}
	})
}
