package argoapi

import (
	"errors"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

type Connection struct {
	URL         string `json:"url"`
	Token       string `json:"token,omitempty"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`
}

func ValidateServerURL(raw string) error {
	return prom.ValidateHTTPBaseURL(raw)
}

func (c Connection) Validate() error {
	if err := ValidateServerURL(c.URL); err != nil {
		return err
	}
	if strings.ContainsAny(c.Token, "\r\n") {
		return errors.New("token must not contain line breaks")
	}
	return nil
}
