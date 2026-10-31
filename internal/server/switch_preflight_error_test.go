package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestWriteSwitchPreflightErrorAddsErrorTypeForUnreachableTarget(t *testing.T) {
	s := &Server{}
	probe := &k8s.ContextProbeError{Context: "staging", Err: fmt.Errorf("cluster unreachable: %w", errors.New("dial tcp 10.0.0.1:6443: connect: connection refused"))}
	rec := httptest.NewRecorder()
	s.writeSwitchPreflightError(rec, "", fmt.Errorf("%w: %w", k8s.ErrContextSwitchPreflight, probe))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["errorType"] != "network" {
		t.Fatalf("errorType = %q, want network (body %v)", body["errorType"], body)
	}
	if body["error"] == "" {
		t.Fatal("error message is empty")
	}
}

func TestWriteSwitchPreflightErrorKeepsPlainErrorForConfigRefusals(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writeSwitchPreflightError(rec, "", fmt.Errorf("%w: context %q is no longer available", k8s.ErrContextSwitchPreflight, "gone"))

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["errorType"]; ok {
		t.Fatalf("config refusal should not carry errorType: %v", body)
	}
}
