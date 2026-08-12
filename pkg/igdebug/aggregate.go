package igdebug

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	maxDNSPending           = 2_000
	maxDNSGroups            = 2_000
	maxDNSAddressesPerGroup = 128
)

type processRow struct {
	Command string `json:"comm"`
	PID     int64  `json:"pid"`
	Parent  struct {
		Command string `json:"comm"`
		PID     int64  `json:"pid"`
	} `json:"parent"`
	Credentials struct {
		User string `json:"user"`
		UID  int64  `json:"uid"`
	} `json:"creds"`
	Runtime struct {
		ContainerID string `json:"containerId"`
	} `json:"runtime"`
}

type endpointRow struct {
	Address  string `json:"addr"`
	Port     uint16 `json:"port"`
	Protocol string `json:"proto"`
	K8s      struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"k8s"`
}

type socketRow struct {
	Source  endpointRow `json:"src"`
	Dest    endpointRow `json:"dst"`
	State   int         `json:"state"`
	Runtime struct {
		ContainerID string `json:"containerId"`
	} `json:"runtime"`
}

type dnsRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	QueryType    string `json:"qtype"`
	QueryOrReply string `json:"qr"`
	ResponseCode string `json:"rcode"`
	Addresses    string `json:"addresses"`
	LatencyNS    int64  `json:"latency_ns_raw"`
	TimestampNS  int64  `json:"timestamp_raw"`
	Runtime      struct {
		ContainerID string `json:"containerId"`
	} `json:"runtime"`
	Source endpointRow `json:"src"`
	Dest   endpointRow `json:"dst"`
}

func ParseProcesses(raw []byte, containerID string) ([]Process, error) {
	var rows []processRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode process snapshot: %w", err)
	}
	result := make([]Process, 0, len(rows))
	for _, row := range rows {
		if row.Runtime.ContainerID != containerID {
			continue
		}
		result = append(result, Process{
			Command:       row.Command,
			PID:           row.PID,
			ParentPID:     row.Parent.PID,
			ParentCommand: row.Parent.Command,
			User:          row.Credentials.User,
			UID:           row.Credentials.UID,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PID < result[j].PID })
	return result, nil
}

func ParseConnections(raw []byte, containerID string) ([]Connection, error) {
	var rows []socketRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode socket snapshot: %w", err)
	}
	result := make([]Connection, 0, len(rows))
	for _, row := range rows {
		if row.Runtime.ContainerID != containerID {
			continue
		}
		result = append(result, Connection{
			Protocol: row.Source.Protocol,
			State:    socketState(row.Source.Protocol, row.State),
			Local:    endpointFromRow(row.Source),
			Remote:   endpointFromRow(row.Dest),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Local.Port != result[j].Local.Port {
			return result[i].Local.Port < result[j].Local.Port
		}
		return result[i].Remote.Address < result[j].Remote.Address
	})
	return result, nil
}

func socketState(protocol string, state int) string {
	if strings.EqualFold(protocol, "UDP") {
		switch state {
		case 1:
			return "CONNECTED"
		case 7:
			return "UNCONNECTED"
		default:
			return fmt.Sprintf("UNKNOWN_%d", state)
		}
	}
	return tcpState(state)
}

func endpointFromRow(row endpointRow) Endpoint {
	result := Endpoint{Address: row.Address, Port: row.Port, Protocol: row.Protocol}
	if row.K8s.Kind != "" && row.K8s.Kind != "raw" {
		result.Resource = &EndpointRef{Kind: row.K8s.Kind, Namespace: row.K8s.Namespace, Name: row.K8s.Name}
	}
	return result
}

func tcpState(state int) string {
	states := map[int]string{
		1: "ESTABLISHED", 2: "SYN_SENT", 3: "SYN_RECV", 4: "FIN_WAIT1",
		5: "FIN_WAIT2", 6: "TIME_WAIT", 7: "CLOSE", 8: "CLOSE_WAIT",
		9: "LAST_ACK", 10: "LISTEN", 11: "CLOSING", 12: "NEW_SYN_RECV",
	}
	if value, ok := states[state]; ok {
		return value
	}
	return fmt.Sprintf("UNKNOWN_%d", state)
}

type DNSAggregator struct {
	containerID string
	queries     map[string]dnsRow
	replies     map[string]struct{}
	groups      map[string]*dnsAccum
	events      int
	truncated   bool
}

type dnsAccum struct {
	group        DNSGroup
	latency      int64
	latencyCount int
}

func NewDNSAggregator(containerID string) *DNSAggregator {
	return &DNSAggregator{
		containerID: containerID,
		queries:     make(map[string]dnsRow),
		replies:     make(map[string]struct{}),
		groups:      make(map[string]*dnsAccum),
	}
}

func (a *DNSAggregator) Add(raw []byte) (bool, error) {
	var row dnsRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return false, fmt.Errorf("decode DNS event: %w", err)
	}
	if row.Runtime.ContainerID != a.containerID {
		return false, nil
	}
	a.events++
	key := dnsKey(row)
	if row.QueryOrReply == "Q" {
		if _, ok := a.replies[key]; ok {
			delete(a.replies, key)
			return true, nil
		}
		if _, exists := a.queries[key]; exists || len(a.queries) < maxDNSPending {
			a.queries[key] = row
		} else {
			a.truncated = true
		}
		return true, nil
	}
	if row.QueryOrReply != "R" {
		return false, nil
	}
	if _, ok := a.queries[key]; ok {
		delete(a.queries, key)
	} else {
		if _, exists := a.replies[key]; exists || len(a.replies) < maxDNSPending {
			a.replies[key] = struct{}{}
		} else {
			a.truncated = true
		}
	}
	groupKey := row.Name + "\x00" + row.QueryType + "\x00" + row.ResponseCode
	accum := a.groups[groupKey]
	if accum == nil {
		if len(a.groups) >= maxDNSGroups {
			a.truncated = true
			return true, nil
		}
		accum = &dnsAccum{group: DNSGroup{Name: row.Name, Type: row.QueryType, ResponseCode: row.ResponseCode}}
		a.groups[groupKey] = accum
	}
	accum.group.Count++
	if row.LatencyNS > 0 {
		accum.latency += row.LatencyNS
		accum.latencyCount++
	}
	latencyMS := float64(row.LatencyNS) / 1_000_000
	if latencyMS > accum.group.MaxLatencyMs {
		accum.group.MaxLatencyMs = latencyMS
	}
	for _, address := range strings.Split(row.Addresses, ",") {
		address = strings.TrimSpace(address)
		if address == "" || contains(accum.group.Addresses, address) {
			continue
		}
		if len(accum.group.Addresses) >= maxDNSAddressesPerGroup {
			a.truncated = true
			continue
		}
		accum.group.Addresses = append(accum.group.Addresses, address)
	}
	return true, nil
}

func dnsKey(row dnsRow) string {
	client, server := row.Source, row.Dest
	if row.QueryOrReply == "R" {
		client, server = row.Dest, row.Source
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s:%d\x00%s:%d\x00%s", row.ID, row.Name, row.QueryType, client.Address, client.Port, server.Address, server.Port, client.Protocol)
}

func (a *DNSAggregator) Result() ([]DNSGroup, int, int, bool) {
	result := make([]DNSGroup, 0, len(a.groups))
	for _, accum := range a.groups {
		group := accum.group
		if accum.latencyCount > 0 {
			group.AverageLatencyMs = float64(accum.latency) / float64(accum.latencyCount) / 1_000_000
		}
		sort.Strings(group.Addresses)
		result = append(result, group)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ResponseCode != result[j].ResponseCode {
			return result[i].ResponseCode < result[j].ResponseCode
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].Type < result[j].Type
	})
	return result, len(a.queries), a.events, a.truncated
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
