package runtimeevidence

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNATSCapturedProgress(t *testing.T) {
	for _, tc := range []struct {
		file, position string
		active         bool
	}{
		{"nats-backlog.json", "0", false},
		{"nats-recovered.json", "5", true},
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
			consumer := facts.NATS.Consumers[0]
			for _, seq := range []*NATSSequence{consumer.Delivered, consumer.AckFloor} {
				if seq == nil || seq.ConsumerSequence != tc.position || seq.StreamSequence != tc.position || (seq.LastActive != "") != tc.active {
					t.Fatalf("unexpected progress: %+v", seq)
				}
			}
			if consumer.OutstandingPullRequests == nil || *consumer.OutstandingPullRequests != 0 {
				t.Fatalf("reported zero missing: %+v", consumer)
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
			if c.Pending != 5 || facts.NATS.Coverage != "node_reported" || c.Delivered != nil || c.AckFloor != nil || c.OutstandingPullRequests != nil {
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
	if c.OutstandingPullRequests == nil || *c.OutstandingPullRequests != 1 {
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
