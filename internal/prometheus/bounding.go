package prometheus

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

// DefaultMaxResponseBytes caps the raw series payload a PromQL surface returns
// before it switches to the cardinality summary from SummarizeLargeResult.
const DefaultMaxResponseBytes = 64 << 10

// MaxResponseBytes returns the raw series payload cap. Tests shrink it through
// RADAR_MCP_PROM_MAX_RESPONSE_BYTES to exercise the summary path.
func MaxResponseBytes() int {
	if v := os.Getenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxResponseBytes
}

// SummarizeLargeResult replaces an oversized series payload with a
// cardinality breakdown plus a ready-to-run rewrite, so the consumer can
// self-correct instead of answering from silently cut data. isRange steers the
// suggestion: with few series the bytes are points-driven, where topk is a
// no-op and a shorter window / larger step is the real fix.
func SummarizeLargeResult(result *prom.QueryResult, query string, isRange bool) json.RawMessage {
	totalPoints := 0
	cardinality := map[string]map[string]struct{}{}
	for _, s := range result.Series {
		totalPoints += len(s.DataPoints)
		for k, v := range s.Labels {
			if cardinality[k] == nil {
				cardinality[k] = map[string]struct{}{}
			}
			cardinality[k][v] = struct{}{}
		}
	}

	type labelCount struct {
		label string
		count int
	}
	counts := make([]labelCount, 0, len(cardinality))
	for k, vals := range cardinality {
		counts = append(counts, labelCount{k, len(vals)})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].count != counts[j].count {
			return counts[i].count > counts[j].count
		}
		return counts[i].label < counts[j].label
	})

	// Hand-built JSON keeps labelCardinality in descending order — the first
	// key is the label that explodes the result, which is the whole point of
	// the summary. A map would marshal in random order.
	var b strings.Builder
	b.WriteString(`{"seriesCount":`)
	b.WriteString(strconv.Itoa(len(result.Series)))
	b.WriteString(`,"totalDataPoints":`)
	b.WriteString(strconv.Itoa(totalPoints))
	b.WriteString(`,"labelCardinality":{`)
	for i, lc := range counts {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(lc.label)
		b.Write(key)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(lc.count))
	}
	b.WriteString(`},"suggestion":`)
	n := len(result.Series)
	topkN := 5
	if n < topkN {
		topkN = n
	}
	var suggestionText string
	if isRange && n <= 5 {
		suggestionText = fmt.Sprintf("only %d series yet still oversized — points-per-series dominate, so topk won't help; use a shorter window (smaller since) or a larger step", n)
	} else {
		suggestionText = fmt.Sprintf("topk(%d, %s)", topkN, query)
	}
	suggestion, _ := json.Marshal(suggestionText)
	b.Write(suggestion)
	b.WriteByte('}')
	return json.RawMessage(b.String())
}
