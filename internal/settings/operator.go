package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sync/atomic"
)

const OperatorFileEnv = "RADAR_OPERATOR_SETTINGS_FILE"

type OperatorConfig struct {
	Version        int          `json:"version"`
	Audit          *AuditConfig `json:"audit,omitempty"`
	HelmOCISources []string     `json:"helmOciSources,omitempty"`
}

var operatorConfig atomic.Pointer[OperatorConfig]

func ReadOperatorConfig(path string) (*OperatorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg OperatorConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid operator settings: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("operator settings must contain one JSON object")
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported operator settings version %d (expected 1)", cfg.Version)
	}
	if cfg.Audit != nil && (cfg.Audit.IgnoredNamespaces == nil || cfg.Audit.DisabledChecks == nil) {
		return nil, fmt.Errorf("operator audit settings require ignoredNamespaces and disabledChecks arrays (use [] for an empty list)")
	}
	return &cfg, nil
}

// The startup snapshot is separate from Load/Update: a personal-preference write
// must never copy deployment-owned values into the writable settings file.
func SetOperatorConfig(cfg *OperatorConfig) {
	if cfg == nil {
		operatorConfig.Store(nil)
		return
	}
	copy := *cfg
	copy.HelmOCISources = slices.Clone(cfg.HelmOCISources)
	if cfg.Audit != nil {
		audit := cloneAudit(*cfg.Audit)
		copy.Audit = &audit
	}
	operatorConfig.Store(&copy)
}

func OperatorConfigured() bool { return operatorConfig.Load() != nil }

func EffectiveAudit() AuditConfig {
	if cfg := operatorConfig.Load(); cfg != nil {
		if cfg.Audit != nil {
			return cloneAudit(*cfg.Audit)
		}
		return DefaultAuditConfig()
	}
	if cfg := Load().Audit; cfg != nil {
		return *cfg
	}
	return DefaultAuditConfig()
}

func EffectiveOCISources() []string {
	if cfg := operatorConfig.Load(); cfg != nil {
		return append([]string{}, cfg.HelmOCISources...)
	}
	return append([]string{}, Load().HelmOCISources...)
}

func cloneAudit(cfg AuditConfig) AuditConfig {
	cfg.IgnoredNamespaces = slices.Clone(cfg.IgnoredNamespaces)
	cfg.DisabledChecks = slices.Clone(cfg.DisabledChecks)
	return cfg
}
