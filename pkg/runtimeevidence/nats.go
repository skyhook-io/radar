package runtimeevidence

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
	Account     string `json:"account"`
	Stream      string `json:"stream"`
	Name        string `json:"name"`
	Pending     uint64 `json:"pending"`
	AckPending  uint64 `json:"ackPending"`
	Redelivered uint64 `json:"redelivered"`
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
					Name        string  `json:"name"`
					Pending     *uint64 `json:"num_pending"`
					AckPending  *uint64 `json:"num_ack_pending"`
					Redelivered *uint64 `json:"num_redelivered"`
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
					result.Consumers = append(result.Consumers, NATSConsumer{Account: account.ID, Stream: stream.Name, Name: consumer.Name, Pending: *consumer.Pending, AckPending: *consumer.AckPending, Redelivered: *consumer.Redelivered})
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
