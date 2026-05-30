// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Command git-proxy-cli is a terminal client for the git-proxy management API
// (D-2 / #76): list held pushes and authorise/reject/cancel them. It is the Go
// port of @finos/git-proxy-cli.
//
// The server is addressed via GIT_PROXY_UI_HOST (default http://localhost) and
// GIT_PROXY_UI_PORT (default 8080); the session is persisted to the file named
// by GIT_PROXY_COOKIE_FILE (default .git-proxy-cli-cookies.json).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dcoric/git-proxy-go/internal/cli"
)

// version is overridden at build time via -ldflags (goreleaser, X-1).
var version = "0.0.0-dev"

const defaultCookieFile = ".git-proxy-cli-cookies.json"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "git-proxy-cli:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("a command is required")
	}

	baseURL := envOr("GIT_PROXY_UI_HOST", "http://localhost") + ":" + envOr("GIT_PROXY_UI_PORT", "8080")
	cookieFile := envOr("GIT_PROXY_COOKIE_FILE", defaultCookieFile)
	cmd, rest := args[0], args[1:]

	newClient := func() (*cli.Client, error) { return cli.New(baseURL, cookieFile) }

	switch cmd {
	case "login":
		return cmdLogin(newClient, rest)
	case "logout":
		return cmdSimple(newClient, rest, "logout", func(c *cli.Client) error { return c.Logout() }, "Logged out")
	case "ls", "list":
		return cmdList(newClient, rest)
	case "authorise", "approve":
		return cmdByID(newClient, rest, "authorise", func(c *cli.Client, id string) error { return c.Authorise(id) }, "Authorised")
	case "cancel":
		return cmdByID(newClient, rest, "cancel", func(c *cli.Client, id string) error { return c.Cancel(id) }, "Canceled")
	case "reject":
		return cmdReject(newClient, rest)
	case "config":
		fmt.Printf("baseUrl:    %s\ncookieFile: %s\nversion:    %s\n", baseURL, cookieFile, version)
		return nil
	case "version", "--version", "-v":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func cmdLogin(newClient func() (*cli.Client, error), args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	username := fs.String("username", "", "username")
	password := fs.String("password", "", "password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" {
		return fmt.Errorf("--username and --password are required")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	if err := c.Login(*username, *password); err != nil {
		return err
	}
	fmt.Printf("Login: '%s': OK\n", *username)
	return nil
}

func cmdList(newClient func() (*cli.Client, error), args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	defs := map[string]*bool{
		"error":      fs.Bool("error", false, `filter by the "error" flag`),
		"blocked":    fs.Bool("blocked", false, `filter by the "blocked" flag`),
		"allowPush":  fs.Bool("allowPush", false, `filter by the "allowPush" flag`),
		"authorised": fs.Bool("authorised", false, `filter by the "authorised" flag`),
		"canceled":   fs.Bool("canceled", false, `filter by the "canceled" flag`),
		"rejected":   fs.Bool("rejected", false, `filter by the "rejected" flag`),
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var f cli.Filters
	// Only flags actually passed become filters (tri-state: unset = don't filter).
	fs.Visit(func(fl *flag.Flag) {
		v := *defs[fl.Name]
		switch fl.Name {
		case "error":
			f.Error = &v
		case "blocked":
			f.Blocked = &v
		case "allowPush":
			f.AllowPush = &v
		case "authorised":
			f.Authorised = &v
		case "canceled":
			f.Canceled = &v
		case "rejected":
			f.Rejected = &v
		}
	})

	c, err := newClient()
	if err != nil {
		return err
	}
	pushes, err := c.List(f)
	if err != nil {
		return err
	}
	if len(pushes) == 0 {
		fmt.Println("No matching pushes.")
		return nil
	}
	cli.PrintPushes(os.Stdout, pushes)
	return nil
}

func cmdReject(newClient func() (*cli.Client, error), args []string) error {
	fs := flag.NewFlagSet("reject", flag.ContinueOnError)
	id := fs.String("id", "", "push ID")
	message := fs.String("message", "Rejected via git-proxy-cli", "reason for rejection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	if err := c.Reject(*id, *message); err != nil {
		return err
	}
	fmt.Printf("Reject: ID '%s': OK\n", *id)
	return nil
}

// cmdByID runs an action that takes a single push --id.
func cmdByID(newClient func() (*cli.Client, error), args []string, name string, action func(*cli.Client, string) error, okVerb string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	id := fs.String("id", "", "push ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	if err := action(c, *id); err != nil {
		return err
	}
	fmt.Printf("%s: ID '%s': OK\n", okVerb, *id)
	return nil
}

// cmdSimple runs an action that takes no arguments.
func cmdSimple(newClient func() (*cli.Client, error), args []string, name string, action func(*cli.Client) error, okMsg string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	if err := action(c); err != nil {
		return err
	}
	fmt.Println(okMsg)
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `git-proxy-cli — terminal client for the git-proxy management API

Usage:
  git-proxy-cli <command> [flags]

Commands:
  login --username U --password P   log in and persist the session
  logout                            log out and clear the session
  ls [--blocked --authorised ...]   list pushes (boolean flag filters)
  authorise --id ID                 approve a held push
  reject --id ID [--message M]      reject a held push
  cancel --id ID                    cancel a held push
  config                            print the resolved configuration
  version                           print the version

Environment:
  GIT_PROXY_UI_HOST   server host (default http://localhost)
  GIT_PROXY_UI_PORT   server port (default 8080)
  GIT_PROXY_COOKIE_FILE  session file (default .git-proxy-cli-cookies.json)
`)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
