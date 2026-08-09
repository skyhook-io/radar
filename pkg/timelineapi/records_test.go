package timelineapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeStreamRoutesByType(t *testing.T) {
	body := strings.Join([]string{
		`{"id":"ev-1","kind":"Deployment","namespace":"default","name":"web"}`,
		``, // blank line tolerated
		`{"type":"coverage","eventTimeStartMs":100,"eventTimeEndMs":200,"reason":"retention"}`,
		`{"id":"ev-2","kind":"Pod","namespace":"default","name":"web-abc"}`,
		`{"type":"end","cursor":"42:7","truncated":true}`,
	}, "\n")

	rows, end, errRec, coverage, err := DecodeStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	if errRec != nil {
		t.Fatalf("unexpected error record: %+v", errRec)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	var e struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rows[0], &e); err != nil || e.ID != "ev-1" || e.Kind != "Deployment" {
		t.Fatalf("row 0 = %s (%v), want ev-1/Deployment", rows[0], err)
	}
	if len(coverage) != 1 || coverage[0].EventTimeStartMs != 100 || coverage[0].EventTimeEndMs != 200 || coverage[0].Reason != "retention" {
		t.Fatalf("coverage = %+v, want one retention gap 100..200", coverage)
	}
	if end == nil || end.Cursor != "42:7" || !end.Truncated || end.More {
		t.Fatalf("terminal = %+v, want cursor 42:7 truncated", end)
	}
}

func TestDecodeStreamInStreamError(t *testing.T) {
	body := strings.Join([]string{
		`{"id":"ev-1","kind":"Pod","namespace":"default","name":"p"}`,
		`{"type":"error","message":"store unavailable"}`,
	}, "\n")

	rows, end, errRec, _, err := DecodeStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if end != nil {
		t.Fatalf("expected no end record, got %+v", end)
	}
	if errRec == nil || errRec.Message != "store unavailable" {
		t.Fatalf("error record = %+v, want store unavailable", errRec)
	}
}

func TestDecodeStreamMalformed(t *testing.T) {
	if _, _, _, _, err := DecodeStream(strings.NewReader("{not json}")); err == nil {
		t.Fatalf("expected error on malformed NDJSON line")
	}
}

// The exported EndRecord must marshal to exactly the on-the-wire terminal
// shape (type/cursor, omitempty flags) the web client parses.
func TestEndRecordJSONShape(t *testing.T) {
	got, err := json.Marshal(EndRecord{Type: RecordTypeEnd, Cursor: "9:3"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"type":"end","cursor":"9:3"}`; string(got) != want {
		t.Fatalf("EndRecord JSON = %s, want %s", got, want)
	}
}
