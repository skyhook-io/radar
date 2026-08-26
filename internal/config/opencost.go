package config

import (
	"fmt"
	"strings"

	"golang.org/x/text/currency"
)

var currentISO4217CodesMissingFromCLDR = map[string]struct{}{
	"MRU": {},
	"SLE": {},
	"UYW": {},
	"VED": {},
	"VES": {},
	"XAD": {},
	"XCG": {},
	"ZWG": {},
}

func NormalizeOpenCostCurrency(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return "", nil
	}
	if code == "XXX" || code == "XTS" {
		return "", fmt.Errorf("must be a monetary ISO 4217 currency code")
	}
	unit, err := currency.ParseISO(code)
	if err != nil {
		if _, ok := currentISO4217CodesMissingFromCLDR[code]; !ok {
			return "", fmt.Errorf("must be a recognized ISO 4217 currency code: %w", err)
		}
		return code, nil
	}
	return unit.String(), nil
}
