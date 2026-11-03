package runtimeevidence

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Adapter string

const (
	RabbitMQ     Adapter = "rabbitmq"
	NATS         Adapter = "nats"
	Vault        Adapter = "vault"
	MaxBodyBytes         = 256 * 1024
	MaxConsumers         = 20
)

type Reason string

func (r Reason) Error() string { return string(r) }

const (
	UnsupportedAdapter   Reason = "unsupported_adapter"
	ResponseTooLarge     Reason = "response_too_large"
	EndpointDenied       Reason = "endpoint_denied"
	RedirectRefused      Reason = "redirect_refused"
	UnexpectedHTTPStatus Reason = "unexpected_http_status"
	UnexpectedShape      Reason = "unexpected_shape"
	MissingAlarmMetrics  Reason = "missing_alarm_metrics"
	InvalidAlarmMetric   Reason = "invalid_alarm_metric"
	AmbiguousAlarmMetric Reason = "ambiguous_alarm_metric"
)

type Facts struct {
	RabbitMQ *RabbitMQFacts `json:"rabbitmq,omitempty"`
	NATS     *NATSFacts     `json:"nats,omitempty"`
	Vault    *VaultFacts    `json:"vault,omitempty"`
}

// Parse interprets one endpoint response, not application or cluster health.
// Errors contain bounded reason codes only; upstream bodies may contain secrets.
func Parse(adapter Adapter, httpStatus int, body []byte) (Facts, error) {
	if len(body) > MaxBodyBytes {
		return Facts{}, ResponseTooLarge
	}
	if httpStatus == http.StatusUnauthorized || httpStatus == http.StatusForbidden {
		return Facts{}, EndpointDenied
	}
	if httpStatus >= 300 && httpStatus < 400 {
		return Facts{}, RedirectRefused
	}
	if httpStatus < 200 || httpStatus > 599 || (adapter != Vault && httpStatus != http.StatusOK) {
		return Facts{}, UnexpectedHTTPStatus
	}
	switch adapter {
	case RabbitMQ:
		facts, err := parseRabbitMQ(body)
		if err != nil {
			return Facts{}, err
		}
		return Facts{RabbitMQ: facts}, nil
	case NATS:
		facts, err := parseNATS(body)
		if err != nil {
			return Facts{}, err
		}
		return Facts{NATS: facts}, nil
	case Vault:
		facts, err := parseVault(body)
		if err != nil {
			return Facts{}, err
		}
		return Facts{Vault: facts}, nil
	default:
		return Facts{}, UnsupportedAdapter
	}
}

func decode(body []byte, target any) error {
	if !utf8.Valid(body) {
		return UnexpectedShape
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if checkJSONValue(decoder, 0) != nil {
		return UnexpectedShape
	}
	if _, err := decoder.Token(); err != io.EOF {
		return UnexpectedShape
	}
	if json.Unmarshal(body, target) != nil {
		return UnexpectedShape
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// encoding/json otherwise silently lets the last conflicting observation win.
func checkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return UnexpectedShape
	}
	token, err := decoder.Token()
	if err != nil {
		return UnexpectedShape
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return UnexpectedShape
			}
			key = strings.ToLower(key)
			if seen[key] {
				return UnexpectedShape
			}
			seen[key] = true
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return UnexpectedShape
	}
	if _, err := decoder.Token(); err != nil {
		return UnexpectedShape
	}
	return nil
}
