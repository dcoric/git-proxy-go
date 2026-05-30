// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"testing"
)

// pktLine encodes s as a git pkt-line (4-hex length prefix + payload).
func pktLine(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}

// objHeader builds a pack object header for the given type code and inflated size.
func objHeader(typ byte, size int) []byte {
	b := (typ << 4) | byte(size&0x0f)
	size >>= 4
	var out []byte
	for size > 0 {
		out = append(out, b|0x80)
		b = byte(size & 0x7f)
		size >>= 7
	}
	return append(out, b)
}

// buildReceivePack builds a receive-pack request body: one ref-update pkt-line,
// a flush packet, then a packfile containing the given commit objects.
func buildReceivePack(t *testing.T, oldSHA, newSHA, ref string, commits ...string) []byte {
	t.Helper()
	var body bytes.Buffer
	body.WriteString(pktLine(oldSHA + " " + newSHA + " " + ref + "\x00report-status"))
	body.WriteString("0000") // flush

	var pack bytes.Buffer
	pack.WriteString(packSignature)
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))            // version
	_ = binary.Write(&pack, binary.BigEndian, uint32(len(commits))) // entries
	for _, c := range commits {
		pack.Write(objHeader(gitObjectCommit, len(c)))
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write([]byte(c)); err != nil {
			t.Fatalf("zlib write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zlib close: %v", err)
		}
		pack.Write(z.Bytes())
	}
	pack.Write(make([]byte, 20)) // fake SHA-1 trailer

	body.Write(pack.Bytes())
	return body.Bytes()
}

func sampleCommit(parent string) string {
	header := "tree 1234567890abcdef1234567890abcdef12345678\n"
	if parent != "" {
		header += "parent " + parent + "\n"
	}
	header += "author Alice <alice@example.com> 1700000000 +0000\n"
	header += "committer Bob <bob@example.com> 1700000001 +0000\n"
	return header + "\nInitial commit"
}

func TestParsePacketLines(t *testing.T) {
	buf := []byte(pktLine("hello") + pktLine("world") + "0000PACKDATA")
	lines, offset, err := parsePacketLines(buf)
	if err != nil {
		t.Fatalf("parsePacketLines: %v", err)
	}
	if len(lines) != 2 || lines[0] != "hello" || lines[1] != "world" {
		t.Errorf("lines = %v, want [hello world]", lines)
	}
	if string(buf[offset:]) != "PACKDATA" {
		t.Errorf("offset points at %q, want PACKDATA", buf[offset:])
	}

	if _, _, err := parsePacketLines([]byte("XXXXdata")); err == nil {
		t.Error("expected error for non-hex length prefix")
	}
}

func TestGetPackMeta(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("PACK")
	_ = binary.Write(&buf, binary.BigEndian, uint32(2))
	_ = binary.Write(&buf, binary.BigEndian, uint32(3))
	buf.WriteString("rest")

	meta, content, err := getPackMeta(buf.Bytes())
	if err != nil {
		t.Fatalf("getPackMeta: %v", err)
	}
	if meta.Sig != "PACK" || meta.Version != 2 || meta.Entries != 3 {
		t.Errorf("meta = %+v", meta)
	}
	if string(content) != "rest" {
		t.Errorf("content = %q, want rest", content)
	}

	if _, _, err := getPackMeta([]byte("short")); err == nil {
		t.Error("expected error for truncated header")
	}
}

func TestGetCommitData(t *testing.T) {
	objs := []gitObject{
		{typeName: "blob", data: "ignored"},
		{typeName: "commit", data: sampleCommit("abc123")},
	}
	commits, err := getCommitData(objs)
	if err != nil {
		t.Fatalf("getCommitData: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %d, want 1 (blobs filtered out)", len(commits))
	}
	c := commits[0]
	if c.Author != "Alice" || c.AuthorEmail != "alice@example.com" {
		t.Errorf("author = %q <%q>", c.Author, c.AuthorEmail)
	}
	if c.Committer != "Bob" || c.CommitterEmail != "bob@example.com" || c.CommitTimestamp != "1700000001" {
		t.Errorf("committer = %q <%q> %q", c.Committer, c.CommitterEmail, c.CommitTimestamp)
	}
	if c.Parent != "abc123" || c.Message != "Initial commit" {
		t.Errorf("parent=%q message=%q", c.Parent, c.Message)
	}

	// No parent header -> empty (zero) hash.
	noParent, _ := getCommitData([]gitObject{{typeName: "commit", data: sampleCommit("")}})
	if noParent[0].Parent != emptyCommitHash {
		t.Errorf("parent = %q, want zero hash", noParent[0].Parent)
	}
}

func TestParsePersonLine(t *testing.T) {
	p, err := parsePersonLine("Alice Smith <alice@example.com> 1700000000 +0100")
	if err != nil {
		t.Fatalf("parsePersonLine: %v", err)
	}
	if p.name != "Alice Smith" || p.email != "alice@example.com" || p.timestamp != "1700000000" {
		t.Errorf("person = %+v", p)
	}
	if _, err := parsePersonLine("no-brackets"); err == nil {
		t.Error("expected error for malformed person line")
	}
}

func TestParsePushFullBody(t *testing.T) {
	const newSHA = "1111111111111111111111111111111111111111"
	const oldSHA = "2222222222222222222222222222222222222222"
	body := buildReceivePack(t, oldSHA, newSHA, "refs/heads/main", sampleCommit("abc123"))

	a := NewAction("id", "push", "POST", 0, "https://github.com/x/y.git")
	e := &Engine{}
	meta, err := e.parsePushInto(context.WithValue(context.Background(), rawBodyKey{}, body), a)
	if err != nil {
		t.Fatalf("parsePushInto: %v", err)
	}
	if meta.Entries != 1 {
		t.Errorf("entries = %d, want 1", meta.Entries)
	}
	if a.Branch != "refs/heads/main" {
		t.Errorf("branch = %q", a.Branch)
	}
	if a.CommitFrom != oldSHA || a.CommitTo != newSHA {
		t.Errorf("commit range = %s..%s", a.CommitFrom, a.CommitTo)
	}
	if len(a.CommitData) != 1 {
		t.Fatalf("commitData = %d, want 1", len(a.CommitData))
	}
	if a.User != "Bob" || a.UserEmail != "bob@example.com" {
		t.Errorf("user = %q <%q>, want Bob/bob@example.com", a.User, a.UserEmail)
	}
}

func TestParsePushEmptyCommitFromUsesParent(t *testing.T) {
	const newSHA = "1111111111111111111111111111111111111111"
	body := buildReceivePack(t, emptyCommitHash, newSHA, "refs/heads/main", sampleCommit("parentsha"))

	a := NewAction("id", "push", "POST", 0, "https://github.com/x/y.git")
	if _, err := (&Engine{}).parsePushInto(rawCtx(body), a); err != nil {
		t.Fatalf("parsePushInto: %v", err)
	}
	if a.CommitFrom != "parentsha" {
		t.Errorf("commitFrom = %q, want parentsha (from the last commit's parent)", a.CommitFrom)
	}
}

func TestParsePushErrors(t *testing.T) {
	e := &Engine{}

	// No body.
	if _, err := e.parsePushInto(rawCtx(nil), NewAction("id", "push", "POST", 0, "u")); err == nil {
		t.Error("expected error for empty body")
	}

	// Two ref updates -> single-branch error.
	var body bytes.Buffer
	body.WriteString(pktLine("a b refs/heads/main\x00caps"))
	body.WriteString(pktLine("c d refs/heads/dev"))
	body.WriteString("0000")
	if _, err := e.parsePushInto(rawCtx(body.Bytes()), NewAction("id", "push", "POST", 0, "u")); err == nil {
		t.Error("expected single-branch error for two ref updates")
	}

	// Missing PACK signature after the flush.
	var noPack bytes.Buffer
	noPack.WriteString(pktLine("a b refs/heads/main\x00caps"))
	noPack.WriteString("0000")
	noPack.WriteString("not-a-pack")
	if _, err := e.parsePushInto(rawCtx(noPack.Bytes()), NewAction("id", "push", "POST", 0, "u")); err == nil {
		t.Error("expected error for missing PACK signature")
	}
}

func rawCtx(body []byte) context.Context {
	return WithRawBody(context.Background(), body)
}

// FuzzParsePush feeds arbitrary bytes through the parser to ensure it never
// panics (parsePush is a hardened entry point — it bounds-checks every offset).
func FuzzParsePush(f *testing.F) {
	f.Add(buildReceivePack(&testing.T{}, "2222222222222222222222222222222222222222",
		"1111111111111111111111111111111111111111", "refs/heads/main", sampleCommit("abc123")))
	f.Add([]byte(""))
	f.Add([]byte("0000PACK"))
	f.Add([]byte(pktLine("a b refs/heads/main") + "0000PACK\x00\x00\x00\x02\x00\x00\x00\x01"))

	e := &Engine{}
	f.Fuzz(func(t *testing.T, body []byte) {
		a := NewAction("id", "push", "POST", 0, "https://github.com/x/y.git")
		// Must not panic regardless of input; an error is fine.
		_, _ = e.parsePushInto(rawCtx(body), a)
	})
}
