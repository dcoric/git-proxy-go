// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// RemoteSession is an exec'd git command on the upstream server.
type RemoteSession struct {
	Stdin   io.WriteCloser
	Stdout  io.Reader
	Stderr  io.Reader
	waitFn  func() (uint32, error)
	closeFn func() error
}

// Wait blocks until the remote command exits, returning its status.
func (r *RemoteSession) Wait() (uint32, error) { return r.waitFn() }

// Close tears down the session and connection.
func (r *RemoteSession) Close() error { return r.closeFn() }

// Upstream connects to an upstream git server over SSH and execs a git command,
// authenticating as the client via their forwarded agent.
type Upstream interface {
	Run(ctx context.Context, host, command string, ag agent.ExtendedAgent) (*RemoteSession, error)
}

// sshUpstream dials upstream git servers with x/crypto/ssh, authenticating with
// the client's forwarded agent and verifying the host key. Port of
// GitProtocol.executeRemoteGitCommand + createSSHConnectionOptions.
type sshUpstream struct {
	hostKey ssh.HostKeyCallback
	timeout time.Duration
}

// NewSSHUpstream builds the upstream dialer with the given host-key callback
// (typically KnownHosts.Callback).
func NewSSHUpstream(hostKey ssh.HostKeyCallback) Upstream {
	return &sshUpstream{hostKey: hostKey, timeout: 30 * time.Second}
}

func (u *sshUpstream) Run(_ context.Context, host, command string, ag agent.ExtendedAgent) (*RemoteSession, error) {
	client, err := ssh.Dial("tcp", net.JoinHostPort(host, "22"), &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(ag.Signers)},
		HostKeyCallback: u.hostKey,
		Timeout:         u.timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", host, err)
	}

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("opening upstream session: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	if err := session.Start(command); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("starting upstream command: %w", err)
	}

	return &RemoteSession{
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
		waitFn: func() (uint32, error) {
			err := session.Wait()
			var ee *ssh.ExitError
			if errors.As(err, &ee) {
				return uint32(ee.ExitStatus()), nil
			}
			if err != nil {
				return 1, err
			}
			return 0, nil
		},
		closeFn: func() error {
			_ = session.Close()
			return client.Close()
		},
	}, nil
}
