// Command rightsizing-replay compares captured Radar recommendations with the
// candidate reduction policy offline. It has no cluster or deployment client.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/skyhook-io/radar/pkg/rightsizing"
)

type comparison struct {
	BaselineDecision rightsizing.Decision `json:"baselineDecision"`
	At               time.Time            `json:"at"`
	Workload         string               `json:"workload"`
	Container        string               `json:"container"`
	Resource         string               `json:"resource"`
	Replicas         int                  `json:"replicas"`
	MinDays          int                  `json:"minDays"`
	Candidate        Result               `json:"candidate"`
	FlatHalf         *float64             `json:"flatHalf,omitempty"`
	Direct           *float64             `json:"direct,omitempty"`
}

func replay(in io.Reader, out io.Writer, minDays int) error {
	if minDays < 1 || minDays > 7 {
		return fmt.Errorf("min-days must be between 1 and 7")
	}
	dec, enc := json.NewDecoder(in), json.NewEncoder(out)
	for index := 1; ; index++ {
		var c Case
		err := dec.Decode(&c)
		if err == io.EOF {
			if index == 1 {
				return fmt.Errorf("no replay cases in input")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("case %d: %w", index, err)
		}
		if c.At.IsZero() || c.Workload == "" || c.Row.Container == "" {
			return fmt.Errorf("case %d: at, workload and row.container are required", index)
		}
		if c.Replicas < 0 || (c.Row.Resource != "cpu" && c.Row.Resource != "memory") {
			return fmt.Errorf("case %d: invalid replicas or row.resource", index)
		}
		result := Evaluate(c, minDays)
		report := comparison{
			At: c.At, Workload: c.Workload, Container: c.Row.Container, Resource: c.Row.Resource,
			Replicas: c.Replicas, MinDays: minDays, BaselineDecision: c.Row.Decision, Candidate: result,
		}
		// Hold eligibility and the demand target fixed to isolate rollout pace.
		if result.eligibleReduction {
			report.Direct = result.Target
			half := max(*result.Target, math.Ceil(*result.Current/2))
			report.FlatHalf = &half
		}
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("write case %d: %w", index, err)
		}
	}
}

func main() {
	minDays := flag.Int("min-days", 2, "minimum effective days required for a shadow reduction (1–7)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: rightsizing-replay [-min-days N] < cases.ndjson > comparisons.ndjson")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := replay(os.Stdin, os.Stdout, *minDays); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
