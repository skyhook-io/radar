package runtimeevidence

import (
	"bytes"
	"math"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type RabbitMQFacts struct {
	DiskAlarm   bool `json:"diskAlarm"`
	MemoryAlarm bool `json:"memoryAlarm"`
}

func parseRabbitMQ(body []byte) (*RabbitMQFacts, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, InvalidAlarmMetric
	}
	read := func(name string) (bool, error) {
		family := families[name]
		if family == nil || len(family.Metric) == 0 {
			return false, MissingAlarmMetrics
		}
		if len(family.Metric) != 1 {
			return false, AmbiguousAlarmMetric
		}
		metric := family.Metric[0]
		var value float64
		switch family.GetType() {
		case dto.MetricType_GAUGE:
			if metric.Gauge == nil || metric.Gauge.Value == nil {
				return false, InvalidAlarmMetric
			}
			value = metric.Gauge.GetValue()
		case dto.MetricType_UNTYPED:
			if metric.Untyped == nil || metric.Untyped.Value == nil {
				return false, InvalidAlarmMetric
			}
			value = metric.Untyped.GetValue()
		default:
			return false, InvalidAlarmMetric
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || (value != 0 && value != 1) {
			return false, InvalidAlarmMetric
		}
		return value == 1, nil
	}
	disk, err := read("rabbitmq_alarms_free_disk_space_watermark")
	if err != nil {
		return nil, err
	}
	memory, err := read("rabbitmq_alarms_memory_used_watermark")
	if err != nil {
		return nil, err
	}
	return &RabbitMQFacts{DiskAlarm: disk, MemoryAlarm: memory}, nil
}
