// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// UserKeyFinder maps a presented SSH public key (authorized_keys form) to its
// owning user (db.FindUserBySSHKey).
type UserKeyFinder interface {
	FindUserBySSHKey(ctx context.Context, key string) (*db.User, error)
}

// GitRequest is a parsed git-over-SSH command for a GitHandler.
type GitRequest struct {
	// Op is "upload-pack" (fetch/clone) or "receive-pack" (push).
	Op string
	// RepoPath is the requested repo, host-qualified, e.g. "github.com/org/repo.git".
	RepoPath string
	Username string
	Channel  ssh.Channel

	// AgentForwarding reports whether the client forwarded its SSH agent (so the
	// handler can authenticate to the upstream as the client). OpenAgent opens
	// that forwarded agent; it is non-nil only when AgentForwarding is true and
	// the caller must Close the returned io.Closer.
	AgentForwarding bool
	OpenAgent       func() (agent.ExtendedAgent, io.Closer, error)
}

// GitHandler runs a parsed git command (routing it through the chain and
// forwarding upstream — P5-2). It returns the process exit status to report to
// the client. The default handler (P5-1) is a placeholder until P5-2 lands.
type GitHandler interface {
	Handle(ctx context.Context, req GitRequest) (exitStatus uint32, err error)
}

// Server is the git-over-SSH server.
type Server struct {
	addr    string
	cfg     *ssh.ServerConfig
	finder  UserKeyFinder
	handler GitHandler
}

// extension key under which the authenticated username travels on the SSH
// connection permissions.
const permUsernameKey = "git-proxy-username"

// NewServer builds the SSH server. handler may be nil, in which case parsed git
// commands get a "not yet implemented" placeholder response (P5-1 standalone).
func NewServer(addr string, hostKey ssh.Signer, finder UserKeyFinder, handler GitHandler) *Server {
	s := &Server{addr: addr, finder: finder, handler: handler}
	if s.handler == nil {
		s.handler = placeholderHandler{}
	}
	s.cfg = &ssh.ServerConfig{PublicKeyCallback: s.authenticate}
	s.cfg.AddHostKey(hostKey)
	return s
}

// authenticate maps the presented public key to a user (publickey is the only
// supported method, mirroring PR #1332). The username is carried on the
// connection permissions.
func (s *Server) authenticate(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	keyString := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	user, err := s.finder.FindUserBySSHKey(context.Background(), keyString)
	if err != nil {
		slog.Error("ssh: error during public key auth", "err", err)
		return nil, fmt.Errorf("authentication error")
	}
	if user == nil {
		return nil, fmt.Errorf("public key not recognised")
	}
	slog.Info("ssh: public key authentication succeeded", "user", user.Username, "remote", conn.RemoteAddr())
	return &ssh.Permissions{Extensions: map[string]string{permUsernameKey: user.Username}}, nil
}

// Serve accepts SSH connections on the configured address until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("ssh listen %s: %w", s.addr, err)
	}
	return s.serve(ctx, ln)
}

// serve accepts connections on ln until ctx is cancelled (the test seam).
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	slog.Info("git SSH listening", "addr", ln.Addr().String())

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				slog.Error("ssh: accept failed", "err", err)
				continue
			}
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, nConn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, s.cfg)
	if err != nil {
		// Handshake/auth failures are expected (unknown keys, scanners).
		slog.Debug("ssh: handshake failed", "err", err)
		_ = nConn.Close()
		return
	}
	defer func() { _ = sshConn.Close() }()

	cs := &connState{conn: sshConn, username: sshConn.Permissions.Extensions[permUsernameKey]}
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			slog.Error("ssh: channel accept failed", "err", err)
			continue
		}
		go s.handleSession(ctx, cs, ch, chReqs)
	}
}

// connState carries per-connection context across a session's requests.
type connState struct {
	conn            *ssh.ServerConn
	username        string
	agentForwarding bool
}

// handleSession services a session channel: one git command per session. It
// also honours an agent-forwarding request (P5-4) so the git command can
// authenticate to the upstream as the client.
func (s *Server) handleSession(ctx context.Context, cs *connState, ch ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case agentRequestType:
			cs.agentForwarding = true
			_ = req.Reply(true, nil)
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				writeExit(ch, 1, "invalid exec request")
				return
			}
			_ = req.Reply(true, nil)
			s.handleExec(ctx, cs, ch, payload.Command)
			return
		default:
			// shell/pty-req/env etc. are unsupported (one git command per session).
			_ = req.Reply(false, nil)
		}
	}
}

func (s *Server) handleExec(ctx context.Context, cs *connState, ch ssh.Channel, command string) {
	op, repoPath, err := parseGitCommand(command)
	if err != nil {
		writeExit(ch, 1, err.Error())
		return
	}
	req := GitRequest{
		Op: op, RepoPath: repoPath, Username: cs.username, Channel: ch,
		AgentForwarding: cs.agentForwarding,
	}
	if cs.agentForwarding {
		req.OpenAgent = func() (agent.ExtendedAgent, io.Closer, error) { return openForwardedAgent(cs.conn) }
	}
	status, err := s.handler.Handle(ctx, req)
	if err != nil {
		slog.Error("ssh: git command failed", "op", op, "repo", repoPath, "user", cs.username, "err", err)
	}
	sendExitStatus(ch, status)
	_ = ch.Close()
}

// gitCommandRegex extracts the repo path from a git-upload-pack/receive-pack
// command (port of the Node regex).
var gitCommandRegex = regexp.MustCompile(`^git-(upload|receive)-pack\s+'?([^']+)'?`)

// parseGitCommand extracts the operation and validated repo path from an SSH
// exec command. Port of handleGitCommand's parsing + validateRepositoryPath.
func parseGitCommand(command string) (op, repoPath string, err error) {
	m := gitCommandRegex.FindStringSubmatch(strings.TrimSpace(command))
	if m == nil {
		return "", "", fmt.Errorf("unsupported command: %s", command)
	}
	op = m[1] + "-pack"
	repoPath = strings.TrimPrefix(m[2], "/")
	if err := validateRepositoryPath(repoPath); err != nil {
		return "", "", err
	}
	return op, repoPath, nil
}

var (
	safeRepoPathRegex = regexp.MustCompile(`^[a-zA-Z0-9._\-/]+\.git$`)
	hostnameRegex     = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)
)

// validateRepositoryPath rejects malformed or traversal-prone repo paths,
// requiring a host/org/repo.git shape. Port of validateRepositoryPath.
func validateRepositoryPath(repoPath string) error {
	if !safeRepoPathRegex.MatchString(repoPath) {
		return fmt.Errorf("invalid repository path format: %s", repoPath)
	}
	if strings.Contains(repoPath, "..") || strings.Contains(repoPath, "//") {
		return fmt.Errorf("invalid repository path: contains path traversal sequences: %s", repoPath)
	}
	segments := strings.Split(repoPath, "/")
	if len(segments) < 3 {
		return fmt.Errorf("invalid repository path: must be host/org/repo.git: %s", repoPath)
	}
	if !hostnameRegex.MatchString(segments[0]) {
		return fmt.Errorf("invalid hostname in repository path: %s", segments[0])
	}
	return nil
}

// placeholderHandler is the default git handler until P5-2 wires the chain.
type placeholderHandler struct{}

func (placeholderHandler) Handle(_ context.Context, req GitRequest) (uint32, error) {
	_, _ = req.Channel.Stderr().Write([]byte("git-proxy: SSH git proxying is not yet enabled\n"))
	return 1, nil
}

func writeExit(ch ssh.Channel, status uint32, msg string) {
	_, _ = ch.Stderr().Write([]byte("git-proxy: " + msg + "\n"))
	sendExitStatus(ch, status)
	_ = ch.Close()
}

// sendExitStatus sends the SSH "exit-status" request the git client expects.
func sendExitStatus(ch ssh.Channel, status uint32) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}
