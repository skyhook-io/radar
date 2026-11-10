package runtimeevidence

import (
	"bytes"
	"math"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type RabbitMQFacts struct {
	DiskAlarm            bool    `json:"diskAlarm"`
	MemoryAlarm          bool    `json:"memoryAlarm"`
	DiskAvailableBytes   *uint64 `json:"diskAvailableBytes,omitempty"`
	DiskWatermarkBytes   *uint64 `json:"diskWatermarkBytes,omitempty"`
	MemoryUsedBytes      *uint64 `json:"memoryUsedBytes,omitempty"`
	MemoryWatermarkBytes *uint64 `json:"memoryWatermarkBytes,omitempty"`
}

func parseRabbitMQ(body []byte) (*RabbitMQFacts, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, InvalidAlarmMetric
	}
	readSample := func(name string) (float64, error) {
		family := families[name]
		if family == nil || len(family.Metric) == 0 {
			return 0, MissingAlarmMetrics
		}
		if len(family.Metric) != 1 {
			return 0, AmbiguousAlarmMetric
		}
		metric := family.Metric[0]
		var value float64
		switch family.GetType() {
		case dto.MetricType_GAUGE:
			if metric.Gauge == nil || metric.Gauge.Value == nil {
				return 0, InvalidAlarmMetric
			}
			value = metric.Gauge.GetValue()
		case dto.MetricType_UNTYPED:
			if metric.Untyped == nil || metric.Untyped.Value == nil {
				return 0, InvalidAlarmMetric
			}
			value = metric.Untyped.GetValue()
		default:
			return 0, InvalidAlarmMetric
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, InvalidAlarmMetric
		}
		return value, nil
	}
	readAlarm := func(name string) (bool, error) {
		value, err := readSample(name)
		if err != nil {
			return false, err
		}
		if value != 0 && value != 1 {
			return false, InvalidAlarmMetric
		}
		return value == 1, nil
	}
	readBytes := func(name string) *uint64 {
		value, err := readSample(name)
		// Keep JSON numbers exact for JavaScript consumers.
		if err != nil || value < 0 || value > 1<<53-1 || math.Trunc(value) != value {
			return nil
		}
		bytes := uint64(value)
		return &bytes
	}
	disk, err := readAlarm("rabbitmq_alarms_free_disk_space_watermark")
	if err != nil {
		return nil, err
	}
	memory, err := readAlarm("rabbitmq_alarms_memory_used_watermark")
	if err != nil {
		return nil, err
	}
	return &RabbitMQFacts{
		DiskAlarm: disk, MemoryAlarm: memory,
		DiskAvailableBytes:   readBytes("rabbitmq_disk_space_available_bytes"),
		DiskWatermarkBytes:   readBytes("rabbitmq_disk_space_available_limit_bytes"),
		MemoryUsedBytes:      readBytes("rabbitmq_process_resident_memory_bytes"),
		MemoryWatermarkBytes: readBytes("rabbitmq_resident_memory_limit_bytes"),
	}, nil
}
