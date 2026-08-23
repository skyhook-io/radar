package config

import (
	"fmt"
	"strings"

	"golang.org/x/text/currency"
)

const defaultOpenCostCurrency = "USD"

func NormalizeOpenCostCurrency(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return defaultOpenCostCurrency, nil
	}
	if code == "XXX" || code == "XTS" {
		return "", fmt.Errorf("must be a monetary ISO 4217 currency code")
	}
	unit, err := currency.ParseISO(code)
	if err != nil {
		return "", fmt.Errorf("must be a recognized ISO 4217 currency code: %w", err)
	}
	return unit.String(), nil
}
