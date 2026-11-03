package runtimeevidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRabbitMQResourceValues(t *testing.T) {
	body := alarms + `
# TYPE rabbitmq_disk_space_available_bytes gauge
rabbitmq_disk_space_available_bytes 15277850624
# TYPE rabbitmq_disk_space_available_limit_bytes gauge
rabbitmq_disk_space_available_limit_bytes 50000000
# TYPE rabbitmq_process_resident_memory_bytes gauge
rabbitmq_process_resident_memory_bytes 134975488
# TYPE rabbitmq_resident_memory_limit_bytes gauge
rabbitmq_resident_memory_limit_bytes 2412409651
`
	facts, reason := Parse(RabbitMQ, 200, []byte(body))
	if reason != nil || facts.RabbitMQ == nil {
		t.Fatalf("Parse = %+v, %s", facts, reason)
	}
	got := facts.RabbitMQ
	for name, pair := range map[string]struct {
		got  *uint64
		want uint64
	}{
		"disk":             {got.DiskAvailableBytes, 15277850624},
		"disk watermark":   {got.DiskWatermarkBytes, 50000000},
		"resident memory":  {got.ResidentMemoryBytes, 134975488},
		"memory watermark": {got.MemoryWatermarkBytes, 2412409651},
	} {
		if pair.got == nil || *pair.got != pair.want {
			t.Errorf("%s = %v, want %d", name, pair.got, pair.want)
		}
	}
	if got.DiskAlarm || got.MemoryAlarm {
		t.Fatal("resource values must not change reported alarms")
	}
}

func TestRabbitMQOptionalBytesDoNotEraseAlarms(t *testing.T) {
	const metric = "rabbitmq_disk_space_available_bytes"
	for _, tc := range []struct {
		name, sample string
		want         *uint64
	}{
		{name: "missing"},
		{name: "zero", sample: metric + " 0\n", want: rabbitBytes(0)},
		{name: "untyped", sample: metric + " 42\n", want: rabbitBytes(42)},
		{name: "single labeled", sample: metric + "{node=\"rabbit@one\"} 42\n", want: rabbitBytes(42)},
		{name: "negative", sample: metric + " -1\n"},
		{name: "fractional", sample: metric + " 0.5\n"},
		{name: "NaN", sample: metric + " NaN\n"},
		{name: "positive infinity", sample: metric + " +Inf\n"},
		{name: "negative infinity", sample: metric + " -Inf\n"},
		{name: "max safe integer", sample: metric + " 9007199254740991\n", want: rabbitBytes(9007199254740991)},
		{name: "unsafe integer", sample: metric + " 9007199254740992\n"},
		{name: "duplicate", sample: metric + " 42\n" + metric + " 42\n"},
		{name: "multiple nodes", sample: metric + "{node=\"one\"} 42\n" + metric + "{node=\"two\"} 43\n"},
		{name: "counter", sample: "# TYPE " + metric + " counter\n" + metric + " 42\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(alarms, "rabbitmq_alarms_free_disk_space_watermark 0", "rabbitmq_alarms_free_disk_space_watermark 1", 1) + tc.sample + "rabbitmq_process_resident_memory_bytes 128\n"
			facts, reason := Parse(RabbitMQ, 200, []byte(body))
			if reason != nil || facts.RabbitMQ == nil {
				t.Fatalf("Parse = %+v, %s", facts, reason)
			}
			got := facts.RabbitMQ
			if !got.DiskAlarm || got.MemoryAlarm || got.ResidentMemoryBytes == nil || *got.ResidentMemoryBytes != 128 {
				t.Fatalf("unrelated evidence lost: %+v", got)
			}
			if (got.DiskAvailableBytes == nil) != (tc.want == nil) || tc.want != nil && *got.DiskAvailableBytes != *tc.want {
				t.Fatalf("disk value = %v, want %v", got.DiskAvailableBytes, tc.want)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && strings.Contains(string(encoded), "diskAvailableBytes") {
				t.Fatalf("unknown value was serialized: %s", encoded)
			}
		})
	}
}

func TestRabbitMQOptionalMetricsCannotHideInvalidRequiredEvidence(t *testing.T) {
	body := "rabbitmq_alarms_free_disk_space_watermark 0\nrabbitmq_process_resident_memory_bytes 128\n"
	facts, reason := Parse(RabbitMQ, 200, []byte(body))
	if reason != MissingAlarmMetrics || facts.RabbitMQ != nil {
		t.Fatalf("Parse = %+v, %s", facts, reason)
	}
	facts, reason = Parse(RabbitMQ, 200, []byte(alarms+"rabbitmq_disk_space_available_bytes{broken 1\n"))
	if reason != InvalidAlarmMetric || facts.RabbitMQ != nil {
		t.Fatalf("malformed document = %+v, %s", facts, reason)
	}
}

func rabbitBytes(value uint64) *uint64 { return &value }
