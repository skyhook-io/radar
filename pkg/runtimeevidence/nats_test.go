package runtimeevidence

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNATSCapturedProgress(t *testing.T) {
	for _, tc := range []struct {
		file, delivered, ackFloor    string
		deliveryActive, ackActive    bool
		pending, ackPending, waiting uint64
	}{
		{"nats-backlog.json", "0", "0", false, false, 5, 0, 0},
		{"nats-recovered.json", "5", "5", true, true, 0, 0, 0},
		{"nats-delivered.json", "5", "0", true, false, 0, 5, 0},
		{"nats-pull-waiting.json", "5", "5", true, true, 0, 0, 1},
	} {
		t.Run(tc.file, func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := Parse(NATS, 200, body)
			if err != nil {
				t.Fatal(err)
			}
			c := facts.NATS.Consumers[0]
			if c.Delivered == nil || c.Delivered.ConsumerSequence != tc.delivered || c.Delivered.StreamSequence != tc.delivered || (c.Delivered.LastActive != "") != tc.deliveryActive {
				t.Fatalf("unexpected delivered: %+v", c.Delivered)
			}
			if c.AckFloor == nil || c.AckFloor.ConsumerSequence != tc.ackFloor || c.AckFloor.StreamSequence != tc.ackFloor || (c.AckFloor.LastActive != "") != tc.ackActive {
				t.Fatalf("unexpected ack floor: %+v", c.AckFloor)
			}
			if c.WaitingPullRequests == nil || *c.WaitingPullRequests != tc.waiting || c.Pending != tc.pending || c.AckPending != tc.ackPending {
				t.Fatalf("unexpected counts: %+v", c)
			}
		})
	}
}

func TestNATSOptionalProgressPreservesBaseFacts(t *testing.T) {
	for _, optional := range []string{
		``,
		`,"delivered":null,"ack_floor":null,"num_waiting":null`,
		`,"delivered":false,"ack_floor":[],"num_waiting":"1"`,
		`,"delivered":{"consumer_seq":-1,"stream_seq":18446744073709551616,"last_active":"never"},"num_waiting":-1`,
		`,"delivered":{"consumer_seq":1.5,"stream_seq":true,"last_active":42},"num_waiting":1.5`,
		`,"num_waiting":9007199254740992`,
		`,"delivered":{"last_active":"2026-11-03T01:44:18.` + strings.Repeat("1", 100) + `Z"}`,
	} {
		t.Run(optional, func(t *testing.T) {
			body := strings.Replace(natsBody(1, 1, 1, 1, 5), `"num_pending":5`, `"num_pending":5`+optional, 1)
			facts, err := Parse(NATS, 200, []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			c := facts.NATS.Consumers[0]
			if c.Pending != 5 || facts.NATS.Coverage != "node_reported" || c.Delivered != nil || c.AckFloor != nil || c.WaitingPullRequests != nil {
				t.Fatalf("invalid optional detail affected facts: %+v", c)
			}
		})
	}
}

func TestNATSProgressExactPositionsAndIndependentFields(t *testing.T) {
	body := strings.Replace(natsBody(1, 1, 1, 1, 5), `"num_pending":5`, `"num_pending":5,"delivered":{"consumer_seq":18446744073709551615,"stream_seq":9007199254740993,"last_active":"bad"},"ack_floor":{"consumer_seq":null,"stream_seq":0,"last_active":"2026-11-03T01:44:18.902568436Z"},"num_waiting":1,"config":{"password":"secret-sentinel"}`, 1)
	facts, err := Parse(NATS, 200, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	c := facts.NATS.Consumers[0]
	if c.Delivered == nil || c.Delivered.ConsumerSequence != "18446744073709551615" || c.Delivered.StreamSequence != "9007199254740993" || c.Delivered.LastActive != "" {
		t.Fatalf("inexact delivered position: %+v", c.Delivered)
	}
	if c.AckFloor == nil || c.AckFloor.ConsumerSequence != "" || c.AckFloor.StreamSequence != "0" || c.AckFloor.LastActive != "2026-11-03T01:44:18.902568436Z" {
		t.Fatalf("valid independent fields lost: %+v", c.AckFloor)
	}
	if c.WaitingPullRequests == nil || *c.WaitingPullRequests != 1 {
		t.Fatalf("pull request count lost: %+v", c)
	}
	output, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"consumerSequence":"18446744073709551615"`) || strings.Contains(string(output), "secret-sentinel") {
		t.Fatalf("unsafe serialized evidence: %s", output)
	}
}

func TestNATSDuplicateProgressKeysRemainUnavailable(t *testing.T) {
	body := strings.Replace(natsBody(1, 1, 1, 1, 5), `"num_pending":5`, `"num_pending":5,"delivered":{"consumer_seq":1,"consumer_seq":2}`, 1)
	facts, err := Parse(NATS, 200, []byte(body))
	if err != UnexpectedShape || facts != (Facts{}) {
		t.Fatalf("ambiguous progress leaked facts: %+v, %v", facts, err)
	}
}

func TestNATSActivityUsesBrowserCompatibleUTC(t *testing.T) {
	for _, input := range []string{"2026-11-03T01:44:18,902568436Z", "2026-11-03T03:44:18.902568436+02:00"} {
		body := strings.Replace(natsBody(1, 1, 1, 1, 5), `"num_pending":5`, `"num_pending":5,"delivered":{"last_active":"`+input+`"}`, 1)
		facts, err := Parse(NATS, 200, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		seq := facts.NATS.Consumers[0].Delivered
		if seq == nil || seq.LastActive != "2026-11-03T01:44:18.902568436Z" {
			t.Fatalf("noncanonical activity: %+v", seq)
		}
	}
}
