// Package timelineapi holds the timeline events wire contract shared by every
// server that speaks it: radar OSS and radar-hub. The HTTP body is NDJSON —
// one JSON object per line — where each line is either an event row or one of
// a small set of control records distinguished by a "type" discriminator. The
// exported record types below pin that on-the-wire shape so both servers, and
// the conformance suite in the wiretest subpackage, decode it the same way.
//
// The package is deliberately dependency-light (standard library only): it is
// the lowest common denominator both implementations import, and it must not
// drag k8s.io or other heavy transitive dependencies into consumers that only
// need to read the framing.
package timelineapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Control-record discriminator values carried in the "type" field. An NDJSON
// line with none of these types (event rows carry no "type" field) is a row.
const (
	RecordTypeEnd      = "end"
	RecordTypeCoverage = "coverage"
	RecordTypeError    = "error"
)

// EndRecord is the terminal record that closes a successful stream. Its shape
// is the contract the web client's retained source parses; radar OSS emits it
// verbatim from internal/server (timelineEndRecord) and radar-hub emits the
// same shape. Cursor is opaque to clients — feed it back as the delta
// position; More signals another delta page waits; Truncated signals a window
// load hit its row cap and the oldest part of the range was cut.
type EndRecord struct {
	Type      string `json:"type"`
	Cursor    string `json:"cursor,omitempty"`
	More      bool   `json:"more,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// CoverageRecord reports a sub-range of the requested window the server could
// not answer for (a retention gap: events aged out, or a period predating the
// current observation). radar OSS never emits it; radar-hub does. Times are
// event-time epoch milliseconds.
type CoverageRecord struct {
	Type             string `json:"type"`
	EventTimeStartMs int64  `json:"eventTimeStartMs,omitempty"`
	EventTimeEndMs   int64  `json:"eventTimeEndMs,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// ErrorRecord is an in-stream failure record: a server that has already
// written rows (and committed 200 + headers) reports a mid-stream failure as a
// terminal line rather than a broken connection. radar OSS surfaces failures
// as real HTTP error statuses before the first byte and never emits this;
// radar-hub emits it.
type ErrorRecord struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

// DecodeStream reads an NDJSON timeline body and routes each line by its "type"
// discriminator: end → terminal, error → errorRec, coverage → coverage, and
// everything else (event rows carry no "type") → rows as raw JSON for the
// caller to unmarshal into its own row type. A successful stream carries
// exactly one terminal (end or error); rows and coverage may repeat. Blank
// lines are skipped. Returns an error only on malformed JSON or a read
// failure — an absent terminal is a caller-level assertion, not a decode
// error, so partial/cut streams remain inspectable.
func DecodeStream(r io.Reader) (rows []json.RawMessage, terminal *EndRecord, errorRec *ErrorRecord, coverage []CoverageRecord, err error) {
	sc := bufio.NewScanner(r)
	// Rows can be large (an event with a diff); lift the line ceiling well above
	// bufio's default 64 KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err = json.Unmarshal(line, &probe); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("timelineapi: malformed NDJSON line %q: %w", line, err)
		}
		switch probe.Type {
		case RecordTypeEnd:
			var end EndRecord
			if err = json.Unmarshal(line, &end); err != nil {
				return nil, nil, nil, nil, fmt.Errorf("timelineapi: malformed end record %q: %w", line, err)
			}
			terminal = &end
		case RecordTypeError:
			var er ErrorRecord
			if err = json.Unmarshal(line, &er); err != nil {
				return nil, nil, nil, nil, fmt.Errorf("timelineapi: malformed error record %q: %w", line, err)
			}
			errorRec = &er
		case RecordTypeCoverage:
			var cov CoverageRecord
			if err = json.Unmarshal(line, &cov); err != nil {
				return nil, nil, nil, nil, fmt.Errorf("timelineapi: malformed coverage record %q: %w", line, err)
			}
			coverage = append(coverage, cov)
		default:
			// Copy: the scanner reuses its buffer on the next Scan.
			rows = append(rows, append(json.RawMessage(nil), line...))
		}
	}
	if err = sc.Err(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("timelineapi: reading NDJSON stream: %w", err)
	}
	return rows, terminal, errorRec, coverage, nil
}
