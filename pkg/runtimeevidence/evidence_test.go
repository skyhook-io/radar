package runtimeevidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

const alarms = "# TYPE rabbitmq_alarms_free_disk_space_watermark gauge\nrabbitmq_alarms_free_disk_space_watermark 0\n# TYPE rabbitmq_alarms_memory_used_watermark gauge\nrabbitmq_alarms_memory_used_watermark 0\n"

func TestRabbitMQAlarmAndRecovery(t *testing.T) {
	for _, active := range []bool{false, true, false} {
		body := alarms
		if active {
			body = strings.Replace(body, "watermark 0", "watermark 1", 1)
		}
		facts, err := Parse(RabbitMQ, 200, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if facts.RabbitMQ == nil || facts.RabbitMQ.DiskAlarm != active || facts.RabbitMQ.MemoryAlarm {
			t.Fatalf("unexpected facts: %+v", facts.RabbitMQ)
		}
	}
}

func TestRabbitMQUnavailableNeverMeansNoAlarms(t *testing.T) {
	tests := []struct {
		name, body string
		reason     Reason
	}{
		{"empty", "", MissingAlarmMetrics},
		{"one alarm absent", "rabbitmq_alarms_free_disk_space_watermark 0\n", MissingAlarmMetrics},
		{"duplicate equal", alarms + "rabbitmq_alarms_free_disk_space_watermark 0\n", AmbiguousAlarmMetric},
		{"multiple labeled nodes", alarms + "rabbitmq_alarms_free_disk_space_watermark{node=\"other\"} 1\n", AmbiguousAlarmMetric},
		{"invalid value", strings.Replace(alarms, "watermark 0", "watermark 2", 1), InvalidAlarmMetric},
		{"nan", strings.Replace(alarms, "watermark 0", "watermark NaN", 1), InvalidAlarmMetric},
		{"infinity", strings.Replace(alarms, "watermark 0", "watermark +Inf", 1), InvalidAlarmMetric},
		{"counter instead of gauge", strings.Replace(alarms, " gauge", " counter", 1), InvalidAlarmMetric},
		{"invalid label", strings.Replace(alarms, "watermark 0", "watermark{node=unquoted} 0", 1), InvalidAlarmMetric},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts, err := Parse(RabbitMQ, 200, []byte(test.body))
			if !errors.Is(err, test.reason) {
				t.Fatalf("got %v, want %v", err, test.reason)
			}
			if facts != (Facts{}) {
				t.Fatalf("failed parser leaked facts: %+v", facts)
			}
		})
	}
}

func TestNATSBacklogRecoveryRetainsMessages(t *testing.T) {
	for _, pending := range []int{5, 0} {
		facts, err := Parse(NATS, 200, []byte(natsBody(1, 1, 1, 1, pending)))
		if err != nil {
			t.Fatal(err)
		}
		n := facts.NATS
		if n.Coverage != "node_reported" || !n.JetStreamEnabled || n.Totals.Messages != 5 || n.Consumers[0].Pending != uint64(pending) || n.Consumers[0].Account != "A0" {
			t.Fatalf("unexpected facts: %+v", n)
		}
	}
}

func TestNATSCoverage(t *testing.T) {
	tests := []struct {
		name, body, coverage string
		rows                 int
		truncated            bool
	}{
		{"disabled", `{"disabled":true}`, "disabled", 0, false},
		{"enabled empty", `{"total":0,"streams":0,"consumers":0,"messages":0}`, "node_reported", 0, false},
		{"missing consumer details", `{"total":1,"streams":1,"consumers":1,"messages":5}`, "partial", 0, false},
		{"missing empty accounts", `{"total":2,"streams":0,"consumers":0,"messages":0}`, "partial", 0, false},
		{"missing streams without consumers", `{"total":0,"streams":2,"consumers":0,"messages":5}`, "partial", 0, false},
		{"row cap", natsBody(1, 1, 21, 21, 5), "partial", 20, true},
		{"changing source counts", natsBody(1, 1, 0, 1, 5), "partial", 1, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts, err := Parse(NATS, 200, []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			n := facts.NATS
			if n.Coverage != test.coverage || len(n.Consumers) != test.rows || n.Truncated != test.truncated {
				t.Fatalf("unexpected facts: %+v", n)
			}
			if n.Coverage == "disabled" && (n.Totals != nil || n.JetStreamEnabled) {
				t.Fatal("disabled should not fabricate totals")
			}
		})
	}
}

func TestNATSAccountIdentity(t *testing.T) {
	var response map[string]any
	if err := json.Unmarshal([]byte(natsBody(2, 2, 2, 1, 5)), &response); err != nil {
		t.Fatal(err)
	}
	accounts := response["account_details"].([]any)
	var second map[string]any
	if err := json.Unmarshal([]byte(`{"id":"A1","name":"same display name","stream_detail":[{"name":"LAB","consumer_detail":[{"name":"idle0","num_pending":2,"num_ack_pending":0,"num_redelivered":0}]}]}`), &second); err != nil {
		t.Fatal(err)
	}
	response["account_details"] = append(accounts, second)
	body, _ := json.Marshal(response)
	facts, err := Parse(NATS, 200, body)
	if err != nil {
		t.Fatal(err)
	}
	if facts.NATS.Coverage != "node_reported" || len(facts.NATS.Consumers) != 2 || facts.NATS.Consumers[0].Account == facts.NATS.Consumers[1].Account {
		t.Fatalf("account identity lost: %+v", facts.NATS)
	}
}

func TestNATSRejectsMissingInvalidAndAmbiguousFacts(t *testing.T) {
	valid := natsBody(1, 1, 1, 1, 5)
	tests := []string{
		`{}`, `null`, `[]`, `{"disabled":"true"}`, `{"total":0,"streams":0,"consumers":0}`,
		strings.Replace(valid, `"num_pending":5`, `"other":5`, 1),
		strings.Replace(valid, `"num_pending":5`, `"num_pending":null`, 1),
		strings.Replace(valid, `"num_pending":5`, `"num_pending":-1`, 1),
		strings.Replace(valid, `"num_pending":5`, `"num_pending":1.5`, 1),
		strings.Replace(valid, `"num_pending":5`, `"num_pending":true`, 1),
		strings.Replace(valid, `"id":"A0"`, `"id":""`, 1),
		strings.Replace(valid, `"id":"A0"`, `"id":"A\n0"`, 1),
		strings.Replace(valid, `"id":"A0"`, `"id":"`+strings.Repeat("a", 257)+`"`, 1),
		strings.Replace(natsBody(1, 1, 2, 2, 5), `"idle1"`, `"idle0"`, 1),
	}
	for i, body := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			facts, err := Parse(NATS, 200, []byte(body))
			if !errors.Is(err, UnexpectedShape) || facts != (Facts{}) {
				t.Fatalf("got %+v, %v", facts, err)
			}
		})
	}
}

func TestVaultReportsFactsIndependentOfStatus(t *testing.T) {
	tests := []struct {
		status                       int
		body                         string
		initialized, sealed, standby bool
	}{
		{501, `{"initialized":false,"sealed":true,"standby":true}`, false, true, true},
		{503, `{"initialized":true,"sealed":true,"standby":true}`, true, true, true},
		{200, `{"initialized":true,"sealed":false,"standby":false}`, true, false, false},
		{429, `{"initialized":true,"sealed":false,"standby":true}`, true, false, true},
		{200, `{"initialized":true,"sealed":true,"standby":true}`, true, true, true},
		{499, `{"initialized":true,"sealed":false,"standby":true}`, true, false, true},
	}
	for _, test := range tests {
		facts, err := Parse(Vault, test.status, []byte(test.body))
		if err != nil {
			t.Fatal(err)
		}
		v := facts.Vault
		if v.Initialized != test.initialized || v.Sealed != test.sealed || v.Standby != test.standby {
			t.Fatalf("unexpected facts: %+v", v)
		}
	}
}

func TestVaultMissingOrMalformedDoesNotBecomeFalse(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"initialized":false,"sealed":false}`, `{"initialized":true,"sealed":null,"standby":false}`, `{"initialized":true,"sealed":"false","standby":false}`, `{"initialized":true,"sealed":false,"standby":false} {}`} {
		facts, err := Parse(Vault, 200, []byte(body))
		if !errors.Is(err, UnexpectedShape) || facts != (Facts{}) {
			t.Fatalf("got %+v, %v", facts, err)
		}
	}
}

func TestFactsNeverIncludeUnselectedFields(t *testing.T) {
	tests := []struct {
		adapter Adapter
		body    string
	}{
		{RabbitMQ, alarms + "irrelevant_metric{password=\"secret-sentinel\"} 1\n"},
		{NATS, strings.Replace(natsBody(1, 1, 1, 1, 5), `"id":"A0"`, `"id":"A0","config":{"password":"secret-sentinel"}`, 1)},
		{Vault, `{"initialized":true,"sealed":false,"standby":false,"token":"secret-sentinel","cluster_name":"secret-sentinel","performance_standby":false}`},
	}
	for _, test := range tests {
		facts, err := Parse(test.adapter, 200, []byte(test.body))
		if err != nil {
			t.Fatal(err)
		}
		output, _ := json.Marshal(facts)
		if strings.Contains(string(output), "secret-sentinel") {
			t.Fatalf("leaked sensitive field: %s", output)
		}
	}
}

func TestEndpointFailuresNeverReturnFacts(t *testing.T) {
	for _, adapter := range []Adapter{RabbitMQ, NATS, Vault} {
		for _, status := range []int{401, 403, 301, 302, 307, 308} {
			facts, err := Parse(adapter, status, []byte(`{"initialized":true,"sealed":false,"standby":false}`))
			if err == nil || facts != (Facts{}) {
				t.Fatalf("%s status %d: %+v %v", adapter, status, facts, err)
			}
		}
	}
	for _, adapter := range []Adapter{RabbitMQ, NATS} {
		if _, err := Parse(adapter, 503, []byte(alarms)); !errors.Is(err, UnexpectedHTTPStatus) {
			t.Fatal(err)
		}
	}
	if _, err := Parse(Vault, 200, []byte(strings.Repeat("x", MaxBodyBytes+1))); !errors.Is(err, ResponseTooLarge) {
		t.Fatal(err)
	}
	if _, err := Parse("other", 200, nil); !errors.Is(err, UnsupportedAdapter) {
		t.Fatal(err)
	}
}

func natsBody(accounts, streams, consumers, rows, pending int) string {
	var details []string
	for i := range rows {
		details = append(details, fmt.Sprintf(`{"name":"idle%d","num_pending":%d,"num_ack_pending":0,"num_redelivered":0}`, i, pending))
	}
	return fmt.Sprintf(`{"total":%d,"streams":%d,"consumers":%d,"messages":5,"account_details":[{"id":"A0","name":"same display name","stream_detail":[{"name":"LAB","consumer_detail":[%s]}]}]}`, accounts, streams, consumers, strings.Join(details, ","))
}

func TestConflictingJSONDoesNotChooseLastValue(t *testing.T) {
	for _, body := range []string{
		`{"initialized":true,"sealed":true,"sealed":false,"standby":false}`,
		`{"initialized":true,"sealed":true,"Sealed":false,"standby":false}`,
		`{"initialized":true,"sealed":true,"s\u0065aled":false,"standby":false}`,
	} {
		facts, err := Parse(Vault, 200, []byte(body))
		if !errors.Is(err, UnexpectedShape) || facts != (Facts{}) {
			t.Fatalf("got %+v %v", facts, err)
		}
	}
}

func TestMalformedJSONIsBoundedAndSanitized(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"initialized":true,"sealed":false,"standby":false,"extra":` + strings.Repeat("[", 66) + strings.Repeat("]", 66) + `}`),
		append([]byte(`{"token":"secret-sentinel`), 0xff),
		[]byte(`{"token":"secret-sentinel"`),
	} {
		facts, err := Parse(Vault, 200, body)
		if !errors.Is(err, UnexpectedShape) || facts != (Facts{}) {
			t.Fatalf("got %+v %v", facts, err)
		}
	}
}

func TestCapturedNATSEndpoints(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		pending uint64
	}{{"backlog", 5}, {"recovered", 0}} {
		body, err := os.ReadFile("testdata/nats-" + scenario.name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		facts, err := Parse(NATS, 200, body)
		if err != nil {
			t.Fatal(err)
		}
		n := facts.NATS
		if n.Coverage != "node_reported" || n.Totals.Messages != 5 || len(n.Consumers) != 1 || n.Consumers[0].Account != "$G" || n.Consumers[0].Pending != scenario.pending {
			t.Fatalf("unexpected facts: %+v", n)
		}
	}
}
