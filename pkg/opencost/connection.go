package opencost

import (
	"errors"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

type Connection struct {
	URL    string `json:"url"`
	APIKey string `json:"apiKey,omitempty"`
}

func (c Connection) Validate() error {
	if err := ValidateKubecostURL(c.URL); err != nil {
		return err
	}
	if strings.ContainsAny(c.APIKey, "\r\n") {
		return errors.New("API key must not contain line breaks")
	}
	return nil
}

func ValidateKubecostURL(raw string) error {
	return prom.ValidateHTTPBaseURL(strings.TrimSpace(raw))
}
