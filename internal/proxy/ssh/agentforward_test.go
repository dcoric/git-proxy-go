// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// agentCheckHandler opens the client's forwarded agent and records the keys it
// advertises, proving the agent-forwarding round-trip works end to end.
type agentCheckHandler struct {
	forwarded chan bool
	keys      chan int
}

func (h *agentCheckHandler) Handle(_ context.Context, req GitRequest) (uint32, error) {
	h.forwarded <- req.AgentForwarding
	if !req.AgentForwarding || req.OpenAgent == nil {
		h.keys <- -1
		return 1, nil
	}
	ag, closer, err := req.OpenAgent()
	if err != nil {
		h.keys <- -1
		return 1, err
	}
	defer func() { _ = closer.Close() }()
	keys, err := ag.List()
	if err != nil {
		h.keys <- -1
		return 1, err
	}
	h.keys <- len(keys)
	return 0, nil
}

func TestAgentForwarding(t *testing.T) {
	clientSigner := genSigner(t)
	keyString := authorizedKey(clientSigner)
	finder := fakeFinder{users: map[string]*db.User{keyString: {Username: "alice"}}}
	handler := &agentCheckHandler{forwarded: make(chan bool, 1), keys: make(chan int, 1)}
	addr := startTestServer(t, finder, handler)

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Forward an in-memory agent holding one key.
	keyring := agent.NewKeyring()
	_, agentPriv, _ := ed25519.GenerateKey(rand.Reader)
	if err := keyring.Add(agent.AddedKey{PrivateKey: &agentPriv}); err != nil {
		t.Fatalf("keyring add: %v", err)
	}
	if err := agent.ForwardToAgent(client, keyring); err != nil {
		t.Fatalf("ForwardToAgent: %v", err)
	}

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = session.Close() }()
	if err := agent.RequestAgentForwarding(session); err != nil {
		t.Fatalf("RequestAgentForwarding: %v", err)
	}
	if err := session.Run("git-upload-pack 'github.com/org/repo.git'"); err != nil {
		t.Fatalf("run: %v", err)
	}

	select {
	case fwd := <-handler.forwarded:
		if !fwd {
			t.Error("server did not see agent forwarding enabled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler not invoked")
	}
	if got := <-handler.keys; got != 1 {
		t.Errorf("forwarded agent advertised %d keys, want 1", got)
	}
}

func TestNoAgentForwarding(t *testing.T) {
	clientSigner := genSigner(t)
	finder := fakeFinder{users: map[string]*db.User{authorizedKey(clientSigner): {Username: "alice"}}}
	handler := &agentCheckHandler{forwarded: make(chan bool, 1), keys: make(chan int, 1)}
	addr := startTestServer(t, finder, handler)

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = session.Close() }()
	// No RequestAgentForwarding: the server must report forwarding disabled.
	_ = session.Run("git-upload-pack 'github.com/org/repo.git'")

	if fwd := <-handler.forwarded; fwd {
		t.Error("server reported agent forwarding without a forwarding request")
	}
}

// authorizedKey renders a signer's public key in authorized_keys form.
func authorizedKey(s ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
}
