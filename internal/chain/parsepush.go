// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// Git pack / pkt-line constants (ported from src/proxy/processors/constants.ts).
const (
	branchPrefix    = "refs/heads/"
	emptyCommitHash = "0000000000000000000000000000000000000000"
	packSignature   = "PACK"
	packetSize      = 4
	gitObjectCommit = 1

	// sevenBitMask / eighthBitMask decode the variable-length size and
	// ofs-delta offset encodings in pack object headers.
	sevenBitMask  = 0x7f
	eighthBitMask = 0x80

	// maxObjectSize bounds a single inflated pack object, so a malicious
	// (zip-bomb) pack cannot exhaust memory. Commits are tiny; this is generous.
	maxObjectSize = 1 << 28 // 256 MiB
)

var personLineRegex = regexp.MustCompile(`^(.*?) <(.*?)> (\d+) ([+-]\d+)$`)

// parsePush parses the receive-pack request body — the pkt-line ref update plus
// the packfile — into the action's commit data and commit range. It is the Go
// port of src/proxy/processors/push-action/parsePush.ts. Parse failures are
// recorded as a step error (which stops the chain), never returned, mirroring
// the Node try/catch.
func (e *Engine) parsePush(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("parsePackFile")
	meta, err := e.parsePushInto(ctx, a)
	if err != nil {
		step.SetError(fmt.Sprintf("Unable to parse push. Please contact an administrator for support: %s", err))
	} else {
		step.SetContent(map[string]any{"meta": meta})
	}
	a.AddStep(step)
	return a, nil
}

func (e *Engine) parsePushInto(ctx context.Context, a *Action) (*packMeta, error) {
	body, _ := RawBody(ctx)
	if len(body) == 0 {
		return nil, fmt.Errorf("no body found in request")
	}

	lines, packDataOffset, err := parsePacketLines(body)
	if err != nil {
		return nil, err
	}
	var refUpdates []string
	for _, l := range lines {
		if strings.Contains(l, branchPrefix) {
			refUpdates = append(refUpdates, l)
		}
	}
	if len(refUpdates) != 1 {
		return nil, fmt.Errorf("your push has been blocked. Please make sure you are pushing to a single branch")
	}

	commitParts := strings.SplitN(refUpdates[0], "\x00", 2)[0]
	parts := strings.Split(commitParts, " ")
	if len(parts) != 3 {
		return nil, fmt.Errorf("your push has been blocked. Invalid ref update format")
	}
	oldCommit, newCommit, ref := parts[0], parts[1], parts[2]

	// Strip any cap-list after a NUL and trim (defensive; commitParts already
	// dropped it).
	if i := strings.IndexByte(ref, 0); i >= 0 {
		ref = ref[:i]
	}
	a.SetBranch(strings.TrimSpace(ref))
	a.SetCommit(oldCommit, newCommit)

	if packDataOffset >= len(body) {
		return nil, fmt.Errorf("your push has been blocked. PACK data is missing")
	}
	buf := body[packDataOffset:]
	if len(buf) < packetSize || string(buf[:packetSize]) != packSignature {
		return nil, fmt.Errorf("your push has been blocked. Invalid PACK data structure")
	}

	meta, contentBuf, err := getPackMeta(buf)
	if err != nil {
		return nil, err
	}
	objects, err := decompressGitObjects(contentBuf, meta.Entries)
	if err != nil {
		return nil, err
	}
	commitData, err := getCommitData(objects)
	if err != nil {
		return nil, err
	}
	a.CommitData = commitData

	if len(commitData) > 0 {
		last := commitData[len(commitData)-1]
		if a.CommitFrom == emptyCommitHash {
			a.CommitFrom = last.Parent
		}
		// Note: this is the last committer's email, not necessarily the pusher's.
		a.User = last.Committer
		a.UserEmail = last.CommitterEmail
	}
	return meta, nil
}

// packMeta is the 12-byte packfile header.
type packMeta struct {
	Sig     string `json:"sig"`
	Version uint32 `json:"version"`
	Entries uint32 `json:"entries"`
}

// getPackMeta reads the packfile header and returns the remaining object bytes.
func getPackMeta(buf []byte) (*packMeta, []byte, error) {
	if len(buf) < 12 {
		return nil, nil, fmt.Errorf("PACK header truncated")
	}
	return &packMeta{
		Sig:     string(buf[0:4]),
		Version: binary.BigEndian.Uint32(buf[4:8]),
		Entries: binary.BigEndian.Uint32(buf[8:12]),
	}, buf[12:], nil
}

// gitObject is one decompressed pack object.
type gitObject struct {
	typeName string
	data     string
}

// decompressGitObjects walks the packed objects, inflating each. Unlike the
// Node byte-at-a-time inflater, this relies on bytes.Reader being an
// io.ByteReader (so flate consumes exactly the deflate stream) to find each
// object boundary. It loops at most meta.Entries times and bounds each object's
// inflated size, so a malformed/hostile pack cannot loop forever or exhaust memory.
func decompressGitObjects(buf []byte, entries uint32) ([]gitObject, error) {
	// Do not pre-size from entries: it comes from the (untrusted) pack header,
	// so a hostile value would force a huge allocation. The loop is bounded by
	// the buffer length regardless (each object consumes at least one byte).
	var objects []gitObject
	offset := 0
	for i := uint32(0); i < entries; i++ {
		if offset >= len(buf) {
			break
		}
		hdr, err := parseGitObjectHeader(buf, offset)
		if err != nil {
			return nil, err
		}
		start := offset + hdr.headerLength
		if start > len(buf) {
			return nil, fmt.Errorf("object header overruns buffer")
		}

		br := bytes.NewReader(buf[start:])
		zr, err := zlib.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("opening zlib stream: %w", err)
		}
		keep := hdr.typeName == "commit"
		data, err := inflateCapped(zr, keep)
		closeErr := zr.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, fmt.Errorf("closing zlib stream: %w", closeErr)
		}
		consumed := br.Size() - int64(br.Len())
		if consumed <= 0 {
			return nil, fmt.Errorf("no progress decoding pack object")
		}
		offset = start + int(consumed)
		objects = append(objects, gitObject{typeName: hdr.typeName, data: data})
	}
	return objects, nil
}

// inflateCapped fully consumes the zlib stream (so the caller can compute how
// many compressed bytes it used) while bounding the inflated size. The content
// is returned only when keep is set (we only need commit bodies).
func inflateCapped(zr io.Reader, keep bool) (string, error) {
	var buf bytes.Buffer
	sink := io.Writer(io.Discard)
	if keep {
		sink = &buf
	}
	n, err := io.Copy(sink, io.LimitReader(zr, maxObjectSize+1))
	if err != nil {
		return "", fmt.Errorf("inflating pack object: %w", err)
	}
	if n > maxObjectSize {
		return "", fmt.Errorf("pack object exceeds size limit")
	}
	return buf.String(), nil
}

// gitObjectHeader holds the parsed pack object header.
type gitObjectHeader struct {
	typeName     string
	headerLength int
}

// parseGitObjectHeader parses the variable-length type+size header (and any
// ofs/ref-delta base reference) of a pack object at offset.
func parseGitObjectHeader(buf []byte, offset int) (gitObjectHeader, error) {
	initial := offset
	if offset >= len(buf) {
		return gitObjectHeader{}, fmt.Errorf("object header truncated")
	}
	b := buf[offset]
	offset++

	typeName := gitObjectType((b >> 4) & 0x07)
	// Variable-length size; we don't use the value but must consume it.
	for b&eighthBitMask != 0 {
		if offset >= len(buf) {
			return gitObjectHeader{}, fmt.Errorf("object size header truncated")
		}
		b = buf[offset]
		offset++
	}

	switch typeName {
	case "ofs_delta":
		_, n, err := parseOfsDeltaOffset(buf, offset)
		if err != nil {
			return gitObjectHeader{}, err
		}
		offset += n
	case "ref_delta":
		if offset+20 > len(buf) {
			return gitObjectHeader{}, fmt.Errorf("ref-delta base sha truncated")
		}
		offset += 20
	}

	return gitObjectHeader{typeName: typeName, headerLength: offset - initial}, nil
}

// parseOfsDeltaOffset decodes the variable-length ofs-delta base offset,
// returning the value and the number of bytes consumed.
func parseOfsDeltaOffset(buf []byte, offset int) (value, length int, err error) {
	i := 0
	if offset >= len(buf) {
		return 0, 0, fmt.Errorf("ofs-delta offset truncated")
	}
	b := buf[offset]
	value = int(b & sevenBitMask)
	for b&eighthBitMask != 0 {
		i++
		if offset+i >= len(buf) {
			return 0, 0, fmt.Errorf("ofs-delta offset truncated")
		}
		b = buf[offset+i]
		value = ((value + 1) << 7) | int(b&sevenBitMask)
	}
	return value, i + 1, nil
}

// gitObjectType maps a pack object type code to its name.
func gitObjectType(code byte) string {
	switch code {
	case 1:
		return "commit"
	case 2:
		return "tree"
	case 3:
		return "blob"
	case 4:
		return "tag"
	case 6:
		return "ofs_delta"
	case 7:
		return "ref_delta"
	default:
		return "unknown"
	}
}

// getCommitData extracts commit objects from the decompressed pack contents,
// parsing each commit's header and message. Port of getCommitData.
func getCommitData(objects []gitObject) ([]db.CommitData, error) {
	var commits []db.CommitData
	for _, obj := range objects {
		if obj.typeName != "commit" {
			continue
		}
		lines := strings.Split(obj.data, "\n")
		headerEnd := -1
		for i, l := range lines {
			if l == "" {
				headerEnd = i
				break
			}
		}
		if headerEnd == -1 {
			headerEnd = len(lines)
		}
		headerLines := lines[:headerEnd]
		message := ""
		if headerEnd+1 <= len(lines) {
			message = strings.TrimSpace(strings.Join(lines[headerEnd+1:], "\n"))
		}

		hdr, err := getParsedData(headerLines)
		if err != nil {
			return nil, err
		}
		parent := emptyCommitHash
		if len(hdr.parents) > 0 {
			parent = hdr.parents[0]
		}
		commits = append(commits, db.CommitData{
			Tree:            hdr.tree,
			Parent:          parent,
			Author:          hdr.author.name,
			Committer:       hdr.committer.name,
			CommitTimestamp: hdr.committer.timestamp,
			Message:         message,
			AuthorEmail:     hdr.author.email,
			CommitterEmail:  hdr.committer.email,
		})
	}
	return commits, nil
}

// personLine is a parsed author/committer line.
type personLine struct {
	name, email, timestamp string
}

// commitHeader is the parsed set of commit header fields.
type commitHeader struct {
	tree      string
	parents   []string
	author    personLine
	committer personLine
}

// getParsedData parses and validates the commit header lines. Port of
// getParsedData + validateParsedData.
func getParsedData(headerLines []string) (commitHeader, error) {
	h := commitHeader{}
	for _, line := range headerLines {
		sp := strings.IndexByte(line, ' ')
		if sp == -1 {
			continue
		}
		key, value := line[:sp], line[sp+1:]
		switch key {
		case "tree":
			if h.tree != "" {
				return h, fmt.Errorf("multiple tree lines found in commit")
			}
			h.tree = strings.TrimSpace(value)
		case "parent":
			h.parents = append(h.parents, strings.TrimSpace(value))
		case "author":
			if !isBlankPersonLine(h.author) {
				return h, fmt.Errorf("multiple author lines found in commit")
			}
			p, err := parsePersonLine(value)
			if err != nil {
				return h, err
			}
			h.author = p
		case "committer":
			if !isBlankPersonLine(h.committer) {
				return h, fmt.Errorf("multiple committer lines found in commit")
			}
			p, err := parsePersonLine(value)
			if err != nil {
				return h, err
			}
			h.committer = p
		}
	}

	var missing []string
	if h.tree == "" {
		missing = append(missing, "tree")
	}
	if isBlankPersonLine(h.author) {
		missing = append(missing, "author")
	}
	if isBlankPersonLine(h.committer) {
		missing = append(missing, "committer")
	}
	if len(missing) > 0 {
		return h, fmt.Errorf("invalid commit data: missing %s", strings.Join(missing, ", "))
	}
	return h, nil
}

func isBlankPersonLine(p personLine) bool {
	return p.name == "" && p.email == "" && p.timestamp == ""
}

// parsePersonLine parses "Name <email> <timestamp> <tz>". Port of parsePersonLine.
func parsePersonLine(line string) (personLine, error) {
	m := personLineRegex.FindStringSubmatch(line)
	if m == nil {
		return personLine{}, fmt.Errorf("failed to parse person line %q: include a name, email, timestamp and timezone offset", line)
	}
	return personLine{name: m[1], email: m[2], timestamp: m[3]}, nil
}

// parsePacketLines splits the leading pkt-lines and returns them plus the offset
// of the byte following the flush packet (where the PACK data begins). Port of
// parsePacketLines.
func parsePacketLines(buf []byte) ([]string, int, error) {
	var lines []string
	offset := 0
	for offset+packetSize <= len(buf) {
		lengthHex := string(buf[offset : offset+packetSize])
		length, err := parseHex16(lengthHex)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid packet line length %s at offset %d", lengthHex, offset)
		}
		if length == 0 {
			offset += packetSize // flush packet
			break
		}
		// A non-flush pkt-line length includes its own 4-byte prefix, so 1..3
		// are invalid (1-3 are reserved/illegal in the git protocol).
		if length < packetSize || offset+length > len(buf) {
			return nil, 0, fmt.Errorf("invalid packet line length %s at offset %d", lengthHex, offset)
		}
		lines = append(lines, string(buf[offset+packetSize:offset+length]))
		offset += length
	}
	return lines, offset, nil
}

// parseHex16 parses a 4-char hex pkt-line length prefix.
func parseHex16(s string) (int, error) {
	var n int
	for _, c := range s {
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, fmt.Errorf("invalid hex %q", s)
		}
		n = n<<4 | d
	}
	return n, nil
}
