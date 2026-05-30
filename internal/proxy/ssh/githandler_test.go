// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/dcoric/git-proxy-go/internal/chain"
	"github.com/dcoric/git-proxy-go/internal/db"
)

// fakeChannel is an in-memory ssh.Channel: the client's bytes are pre-loaded in
// `in`; data written to the client lands in `out`, stderr in `errOut`.
type fakeChannel struct {
	in     *bytes.Buffer
	out    bytes.Buffer
	errOut bytes.Buffer
	mu     sync.Mutex
}

func newFakeChannel(clientData []byte) *fakeChannel {
	return &fakeChannel{in: bytes.NewBuffer(clientData)}
}

func (c *fakeChannel) Read(p []byte) (int, error) { return c.in.Read(p) }
func (c *fakeChannel) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}
func (c *fakeChannel) Close() error      { return nil }
func (c *fakeChannel) CloseWrite() error { return nil }
func (c *fakeChannel) SendRequest(string, bool, []byte) (bool, error) {
	return true, nil
}
func (c *fakeChannel) Stderr() io.ReadWriter { return &c.errOut }
func (c *fakeChannel) outBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.out.Bytes()...)
}

// fakeEngine records the synthesised request and returns a canned action. It
// reads RawBody/SSHCloneAuth from the ctx Execute is called with (not the
// request's), so the tests verify the handler hands the chain a context that
// actually carries the pack and clone credentials.
type fakeEngine struct {
	action     *chain.Action
	gotReq     *http.Request
	gotBody    []byte
	gotSSHAuth *chain.SSHCloneAuth
	hasSSHAuth bool
	called     bool
}

func (e *fakeEngine) Execute(ctx context.Context, r *http.Request) *chain.Action {
	e.called = true
	e.gotReq = r
	if b, ok := chain.RawBody(ctx); ok {
		e.gotBody = b
	}
	if auth, ok := chain.SSHCloneAuthFromContext(ctx); ok {
		e.gotSSHAuth = auth
		e.hasSSHAuth = true
	}
	return e.action
}

// fakeUpstream returns a session backed by buffers, recording the dial details.
type fakeUpstream struct {
	host, command string
	called        bool
	stdout        []byte // bytes the upstream "sends" (caps + report-status)
	stdin         bytes.Buffer
	err           error
}

func (u *fakeUpstream) Run(_ context.Context, host, command string, _ agent.ExtendedAgent) (*RemoteSession, error) {
	u.called = true
	u.host = host
	u.command = command
	if u.err != nil {
		return nil, u.err
	}
	return &RemoteSession{
		Stdin:   nopWriteCloser{&u.stdin},
		Stdout:  bytes.NewReader(u.stdout),
		Stderr:  bytes.NewReader(nil),
		waitFn:  func() (uint32, error) { return 0, nil },
		closeFn: func() error { return nil },
	}, nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// stubHostKey is a non-nil host-key callback for handler tests (the fake engine
// never clones, so it is not invoked).
func stubHostKey(string, net.Addr, ssh.PublicKey) error { return nil }

// forwardingRequest builds a GitRequest with agent forwarding satisfied by an
// in-memory keyring (a real ExtendedAgent so the handler can read its Signers).
func forwardingRequest(op, repoPath string, ch ssh.Channel) GitRequest {
	ag := agent.NewKeyring().(agent.ExtendedAgent)
	return GitRequest{
		Op: op, RepoPath: repoPath, Username: "alice", Channel: ch,
		AgentForwarding: true,
		OpenAgent:       func() (agent.ExtendedAgent, io.Closer, error) { return ag, nopCloser{}, nil },
	}
}

// pktLine frames a git pkt-line (4-hex length prefix including the header).
func pktLine(payload string) []byte {
	return fmt.Appendf(nil, "%04x%s", len(payload)+4, payload)
}

// refAdvertisement is a minimal correctly-framed receive-pack ref advertisement.
func refAdvertisement() []byte {
	line := pktLine("0000000000000000000000000000000000000000 capabilities^{}\x00report-status\n")
	return append(line, []byte("0000")...)
}

func okAction() *chain.Action { return &chain.Action{} }
func blockedAction() *chain.Action {
	msg := "you do not have push permission"
	return &chain.Action{Push: db.Push{Blocked: true, BlockedMessage: &msg}}
}

func TestHandleRequiresAgentForwarding(t *testing.T) {
	engine := &fakeEngine{action: okAction()}
	up := &fakeUpstream{}
	h := NewProxyHandler(engine, up, stubHostKey)
	ch := newFakeChannel(nil)

	req := GitRequest{Op: "upload-pack", RepoPath: "github.com/org/repo.git", Channel: ch, AgentForwarding: false}
	status, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 1 {
		t.Errorf("status = %d, want 1", status)
	}
	if up.called {
		t.Error("upstream dialed despite missing agent forwarding")
	}
	if !bytes.Contains(ch.errOut.Bytes(), []byte("agent forwarding is required")) {
		t.Errorf("client not told agent forwarding is required: %q", ch.errOut.String())
	}
}

func TestHandlePullBlocked(t *testing.T) {
	engine := &fakeEngine{action: blockedAction()}
	up := &fakeUpstream{}
	h := NewProxyHandler(engine, up, stubHostKey)
	ch := newFakeChannel(nil)

	status, err := h.Handle(context.Background(), forwardingRequest("upload-pack", "github.com/org/repo.git", ch))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 1 {
		t.Errorf("status = %d, want 1", status)
	}
	if up.called {
		t.Error("upstream dialed for a blocked pull")
	}
	if !engine.called {
		t.Error("pull chain not executed")
	}
	if !bytes.Contains(ch.errOut.Bytes(), []byte("you do not have push permission")) {
		t.Errorf("block message not relayed: %q", ch.errOut.String())
	}
	// Pull chain runs a GET upload-pack request, no body.
	if engine.gotReq.Method != http.MethodGet {
		t.Errorf("pull method = %s, want GET", engine.gotReq.Method)
	}
}

func TestHandlePullProxiesWhenAllowed(t *testing.T) {
	engine := &fakeEngine{action: okAction()}
	up := &fakeUpstream{stdout: []byte("PACK-from-upstream")}
	h := NewProxyHandler(engine, up, stubHostKey)
	ch := newFakeChannel([]byte("0032want ...\n0000")) // client fetch request

	status, err := h.Handle(context.Background(), forwardingRequest("upload-pack", "github.com/org/repo.git", ch))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 0 {
		t.Errorf("status = %d, want 0", status)
	}
	if !up.called {
		t.Fatal("upstream not dialed for an allowed pull")
	}
	if up.host != "github.com" || up.command != "git-upload-pack '/org/repo.git'" {
		t.Errorf("upstream dial = %s %q", up.host, up.command)
	}
	if got := ch.outBytes(); !bytes.Equal(got, []byte("PACK-from-upstream")) {
		t.Errorf("upstream stdout not relayed to client: %q", got)
	}
}

func TestHandlePushBlockedNotForwarded(t *testing.T) {
	// The client sends only its commands + pack; the caps come from upstream.
	client := []byte("PACK-client-data")

	engine := &fakeEngine{action: blockedAction()}
	up := &fakeUpstream{stdout: refAdvertisement()}
	h := NewProxyHandler(engine, up, stubHostKey)
	ch := newFakeChannel(client)

	status, err := h.Handle(context.Background(), forwardingRequest("receive-pack", "github.com/org/repo.git", ch))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 1 {
		t.Errorf("status = %d, want 1", status)
	}
	if !engine.called {
		t.Fatal("push chain not executed")
	}
	if engine.gotReq.Method != http.MethodPost {
		t.Errorf("push method = %s, want POST", engine.gotReq.Method)
	}
	if len(engine.gotBody) == 0 {
		t.Error("pack data not passed to the chain via RawBody")
	}
	if up.stdin.Len() != 0 {
		t.Errorf("blocked pack was forwarded upstream: %q", up.stdin.String())
	}
	if !bytes.Contains(ch.errOut.Bytes(), []byte("you do not have push permission")) {
		t.Errorf("block message not relayed: %q", ch.errOut.String())
	}
}

func TestHandlePushForwardsWhenAllowed(t *testing.T) {
	caps := refAdvertisement()
	report := append(pktLine("unpack ok\n"), []byte("0000")...)
	client := []byte("PACK-client-data")

	engine := &fakeEngine{action: okAction()}
	up := &fakeUpstream{stdout: append(append([]byte(nil), caps...), report...)}
	h := NewProxyHandler(engine, up, stubHostKey)
	ch := newFakeChannel(client)

	status, err := h.Handle(context.Background(), forwardingRequest("receive-pack", "github.com/org/repo.git", ch))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 0 {
		t.Errorf("status = %d, want 0", status)
	}
	if up.command != "git-receive-pack '/org/repo.git'" {
		t.Errorf("upstream command = %q", up.command)
	}
	// The chain must receive the pack on the ctx it is executed with (the bug
	// fix: previously Execute got a context without the RawBody).
	if !bytes.Equal(engine.gotBody, client) {
		t.Errorf("chain RawBody = %q, want %q", engine.gotBody, client)
	}
	// The forwarded-agent credentials must reach the chain so pullRemote can
	// clone over SSH (#105).
	if !engine.hasSSHAuth {
		t.Fatal("SSHCloneAuth not placed on the chain context")
	}
	if engine.gotSSHAuth.User != "git" || engine.gotSSHAuth.Signers == nil || engine.gotSSHAuth.HostKey == nil {
		t.Errorf("SSHCloneAuth incomplete: %+v", engine.gotSSHAuth)
	}
	// The approved pack (everything the client sent after the caps) is forwarded.
	if !bytes.Contains(up.stdin.Bytes(), []byte("PACK-client-data")) {
		t.Errorf("approved pack not forwarded upstream: %q", up.stdin.String())
	}
	if got := ch.outBytes(); !bytes.Contains(got, report) {
		t.Errorf("report-status not relayed to client: %q", got)
	}
}

func TestChainRequestSynthesis(t *testing.T) {
	t.Run("receive-pack", func(t *testing.T) {
		r := chainRequest(context.Background(), "receive-pack", "github.com/org/repo.git", []byte("pack"))
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.RequestURI != "/github.com/org/repo.git/git-receive-pack" {
			t.Errorf("RequestURI = %s", r.RequestURI)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-git-receive-pack-request" {
			t.Errorf("Content-Type = %s", got)
		}
		if got := r.Header.Get("User-Agent"); got != userAgentSSH {
			t.Errorf("User-Agent = %s", got)
		}
		body, ok := chain.RawBody(r.Context())
		if !ok || string(body) != "pack" {
			t.Errorf("RawBody = %q, %v", body, ok)
		}
	})
	t.Run("upload-pack has no body", func(t *testing.T) {
		r := chainRequest(context.Background(), "upload-pack", "github.com/org/repo.git", nil)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-git-upload-pack-request" {
			t.Errorf("Content-Type = %s", got)
		}
		if _, ok := chain.RawBody(r.Context()); ok {
			t.Error("upload-pack request should carry no raw body")
		}
	})
}

func TestSplitHostRepo(t *testing.T) {
	tests := []struct{ in, host, repo string }{
		{"github.com/org/repo.git", "github.com", "/org/repo.git"},
		{"gitlab.com/group/sub/repo.git", "gitlab.com", "/group/sub/repo.git"},
		{"noslash", "noslash", "/"},
	}
	for _, tc := range tests {
		host, repo := splitHostRepo(tc.in)
		if host != tc.host || repo != tc.repo {
			t.Errorf("splitHostRepo(%q) = %q,%q want %q,%q", tc.in, host, repo, tc.host, tc.repo)
		}
	}
}

func TestReadRefAdvertisement(t *testing.T) {
	adv := []byte("0008abc\n0000")
	trailing := []byte("PACKDATA")
	r := bytes.NewReader(append(append([]byte(nil), adv...), trailing...))

	got, err := readRefAdvertisement(r)
	if err != nil {
		t.Fatalf("readRefAdvertisement: %v", err)
	}
	if !bytes.Equal(got, adv) {
		t.Errorf("advertisement = %q, want %q", got, adv)
	}
	// The reader must be left positioned at the trailing pack data.
	rest, _ := io.ReadAll(r)
	if !bytes.Equal(rest, trailing) {
		t.Errorf("trailing data = %q, want %q", rest, trailing)
	}
}
