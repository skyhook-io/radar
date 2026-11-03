package runtimeevidence

import (
	"encoding/json"
	"strconv"
	"time"
)

type NATSFacts struct {
	JetStreamEnabled  bool           `json:"jetStreamEnabled"`
	Totals            *NATSTotals    `json:"totals,omitempty"`
	Consumers         []NATSConsumer `json:"consumers"`
	Coverage          string         `json:"coverage"`
	ReturnedAccounts  int            `json:"returnedAccounts"`
	ReturnedStreams   int            `json:"returnedStreams"`
	ReturnedConsumers int            `json:"returnedConsumers"`
	Truncated         bool           `json:"truncated"`
}

type NATSTotals struct {
	Accounts  uint64 `json:"accounts"`
	Streams   uint64 `json:"streams"`
	Consumers uint64 `json:"consumers"`
	Messages  uint64 `json:"messages"`
}

type NATSConsumer struct {
	Account                 string        `json:"account"`
	Stream                  string        `json:"stream"`
	Name                    string        `json:"name"`
	Pending                 uint64        `json:"pending"`
	AckPending              uint64        `json:"ackPending"`
	Redelivered             uint64        `json:"redelivered"`
	Delivered               *NATSSequence `json:"delivered,omitempty"`
	AckFloor                *NATSSequence `json:"ackFloor,omitempty"`
	OutstandingPullRequests *uint64       `json:"outstandingPullRequests,omitempty"`
}

type NATSSequence struct {
	// Decimal strings preserve uint64 sequence identifiers in JavaScript clients.
	ConsumerSequence string `json:"consumerSequence,omitempty"`
	StreamSequence   string `json:"streamSequence,omitempty"`
	LastActive       string `json:"lastActive,omitempty"`
}

func parseNATS(body []byte) (*NATSFacts, error) {
	var response struct {
		Disabled       *bool   `json:"disabled"`
		Accounts       *uint64 `json:"total"`
		Streams        *uint64 `json:"streams"`
		Consumers      *uint64 `json:"consumers"`
		Messages       *uint64 `json:"messages"`
		AccountDetails []struct {
			ID      string `json:"id"`
			Streams []struct {
				Name      string `json:"name"`
				Consumers []struct {
					Name        string          `json:"name"`
					Pending     *uint64         `json:"num_pending"`
					AckPending  *uint64         `json:"num_ack_pending"`
					Redelivered *uint64         `json:"num_redelivered"`
					Delivered   json.RawMessage `json:"delivered"`
					AckFloor    json.RawMessage `json:"ack_floor"`
					Waiting     json.RawMessage `json:"num_waiting"`
				} `json:"consumer_detail"`
			} `json:"stream_detail"`
		} `json:"account_details"`
	}
	if err := decode(body, &response); err != nil {
		return nil, err
	}
	result := &NATSFacts{Consumers: []NATSConsumer{}, Coverage: "disabled"}
	if response.Disabled != nil && *response.Disabled {
		return result, nil
	}
	if response.Accounts == nil || response.Streams == nil || response.Consumers == nil || response.Messages == nil {
		return nil, UnexpectedShape
	}
	result.JetStreamEnabled = true
	result.Totals = &NATSTotals{Accounts: *response.Accounts, Streams: *response.Streams, Consumers: *response.Consumers, Messages: *response.Messages}
	result.Coverage = "node_reported"
	seenAccounts := make(map[string]bool)
	for _, account := range response.AccountDetails {
		if !validIdentifier(account.ID) || seenAccounts[account.ID] {
			return nil, UnexpectedShape
		}
		seenAccounts[account.ID] = true
		result.ReturnedAccounts++
		seenStreams := make(map[string]bool)
		for _, stream := range account.Streams {
			if !validIdentifier(stream.Name) || seenStreams[stream.Name] {
				return nil, UnexpectedShape
			}
			seenStreams[stream.Name] = true
			result.ReturnedStreams++
			seenConsumers := make(map[string]bool)
			for _, consumer := range stream.Consumers {
				if !validIdentifier(consumer.Name) || seenConsumers[consumer.Name] || consumer.Pending == nil || consumer.AckPending == nil || consumer.Redelivered == nil {
					return nil, UnexpectedShape
				}
				seenConsumers[consumer.Name] = true
				result.ReturnedConsumers++
				if len(result.Consumers) < MaxConsumers {
					item := NATSConsumer{Account: account.ID, Stream: stream.Name, Name: consumer.Name, Pending: *consumer.Pending, AckPending: *consumer.AckPending, Redelivered: *consumer.Redelivered,
						Delivered: parseNATSSequence(consumer.Delivered), AckFloor: parseNATSSequence(consumer.AckFloor)}
					var waiting *uint64
					// Counts remain JSON numbers, so values outside JavaScript exact integer range are unavailable.
					if json.Unmarshal(consumer.Waiting, &waiting) == nil && waiting != nil && *waiting <= 1<<53-1 {
						item.OutstandingPullRequests = waiting
					}
					result.Consumers = append(result.Consumers, item)
				}
			}
		}
	}
	result.Truncated = result.ReturnedConsumers > MaxConsumers
	if result.Truncated || uint64(result.ReturnedAccounts) != *response.Accounts || uint64(result.ReturnedStreams) != *response.Streams || uint64(result.ReturnedConsumers) != *response.Consumers {
		result.Coverage = "partial"
	}
	return result, nil
}

func parseNATSSequence(raw json.RawMessage) *NATSSequence {
	var value struct {
		Consumer   json.RawMessage `json:"consumer_seq"`
		Stream     json.RawMessage `json:"stream_seq"`
		LastActive json.RawMessage `json:"last_active"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	result := &NATSSequence{ConsumerSequence: natsSequenceNumber(value.Consumer), StreamSequence: natsSequenceNumber(value.Stream)}
	var lastActive string
	if json.Unmarshal(value.LastActive, &lastActive) == nil && len(lastActive) <= len("2006-01-02T15:04:05.999999999+07:00") {
		if _, err := time.Parse(time.RFC3339Nano, lastActive); err == nil {
			result.LastActive = lastActive
		}
	}
	if *result == (NATSSequence{}) {
		return nil
	}
	return result
}

func natsSequenceNumber(raw json.RawMessage) string {
	var value *uint64
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return ""
	}
	return strconv.FormatUint(*value, 10)
}
