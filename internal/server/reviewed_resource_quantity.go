package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Missing values compare by value in the action snapshot and serialize as
// null on responses. On requests, omission is the only way to retain a field.
type workloadResourceValue string

func (v *workloadResourceValue) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil || s == "" {
		return errors.New("resource action fields must be nonempty strings; omit a request or limit to keep it")
	}
	*v = workloadResourceValue(s)
	return nil
}

func (v workloadResourceValue) MarshalJSON() ([]byte, error) {
	if v == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(v))
}

type containerResourcePair struct {
	Request workloadResourceValue `json:"request"`
	Limit   workloadResourceValue `json:"limit"`
}

var desiredQuantityRE = regexp.MustCompile(`^([0-9]+(?:\.[0-9]{1,9})?|\.[0-9]{1,9})([a-zA-Z]*)$`)

var memoryUnitBytes = map[string]int64{
	"": 1, "k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18,
	"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50, "Ei": 1 << 60,
}

// Parse the exact decimal before converting to a Quantity: Kubernetes rounds
// sub-nano values and saturates BinarySI above MaxInt64.
func desiredResourceValue(raw workloadResourceValue, dimension string) (workloadResourceValue, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 40 {
		return "", errors.New("a resource quantity may contain up to 40 characters")
	}
	match := desiredQuantityRE.FindStringSubmatch(string(raw))
	if match == nil {
		return "", fmt.Errorf("invalid %s quantity; use a positive decimal with a Kubernetes unit", dimension)
	}
	unit := memoryUnitBytes[match[2]]
	if dimension == "cpu" {
		switch match[2] {
		case "":
			unit = 1000
		case "m":
			unit = 1
		default:
			unit = 0
		}
	}
	number, ok := new(big.Rat).SetString(match[1])
	if !ok || unit == 0 {
		return "", fmt.Errorf("invalid %s unit", dimension)
	}
	number.Mul(number, new(big.Rat).SetInt64(unit))
	if !number.IsInt() || number.Sign() <= 0 || !number.Num().IsInt64() {
		return "", fmt.Errorf("%s must be positive, fit in 64 bits and be exact in millicores or bytes", dimension)
	}
	return canonicalResourceValue(number.Num().Int64(), dimension), nil
}

func canonicalResourceValue(n int64, dimension string) workloadResourceValue {
	if dimension == "cpu" {
		return workloadResourceValue(resource.NewMilliQuantity(n, resource.DecimalSI).String())
	}
	decimal := resource.NewQuantity(n, resource.DecimalSI).String()
	binary := resource.NewQuantity(n, resource.BinarySI).String()
	if len(binary) <= len(decimal) {
		return workloadResourceValue(binary)
	}
	return workloadResourceValue(decimal)
}

// Kubernetes may return a different valid unit than the desired input. Limit
// exponent size before invoking arbitrary-precision arithmetic, even on reads.
var liveQuantityExponent = regexp.MustCompile(`[eE]([+-]?[0-9]+)$`)

func liveResourceValue(raw, dimension string) (workloadResourceValue, error) {
	if len(raw) > 64 {
		return "", errors.New("live resource quantity exceeds the supported range")
	}
	if exponent := liveQuantityExponent.FindStringSubmatch(raw); exponent != nil && len(strings.TrimLeft(exponent[1], "+-")) > 2 {
		return "", errors.New("live resource exponent exceeds the supported range")
	}
	q, err := resource.ParseQuantity(raw)
	if err != nil || q.Sign() < 0 {
		return "", errors.New("Kubernetes returned an invalid resource quantity")
	}
	scale := resource.Scale(0)
	bound := resource.NewQuantity(math.MaxInt64, resource.DecimalSI)
	if dimension == "cpu" {
		scale = resource.Milli
		bound = resource.NewMilliQuantity(math.MaxInt64, resource.DecimalSI)
	}
	if _, exact := q.AsScale(scale); !exact || q.Cmp(*bound) > 0 {
		return "", fmt.Errorf("live %s cannot be represented exactly in millicores or bytes", dimension)
	}
	return canonicalResourceValue(q.ScaledValue(scale), dimension), nil
}
