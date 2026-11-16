package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

const (
	maxVeleroCompressedBytes = 16 << 20
	maxVeleroDecodedBytes    = 64 << 20
	maxVeleroLogLineBytes    = 64 << 10
	maxVeleroArtifactRows    = 5000
)

var (
	errVeleroCompressedLimit = errors.New("Velero compressed size limit reached")
	errVeleroDecodedLimit    = errors.New("Velero decoded size limit reached")
)

// Unlike LimitReader's EOF, this distinguishes a complete file from a prefix.
type veleroLimitReader struct {
	reader     io.Reader
	remaining  int64
	limitError error
}

func (r *veleroLimitReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, r.limitError
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

type veleroContentItem struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Action    string `json:"action,omitempty"`
}

type veleroContentGroup struct {
	Kind    string              `json:"kind"`
	Count   int                 `json:"count"`
	Matched int                 `json:"matched"`
	Items   []veleroContentItem `json:"items"`
}

type veleroRunContents struct {
	Groups     []veleroContentGroup `json:"groups"`
	Total      int                  `json:"total"`
	Matched    int                  `json:"matched"`
	Namespaces []string             `json:"namespaces"`
	Actions    map[string]int       `json:"actions"`
	Truncated  bool                 `json:"truncated"`
}

type veleroContentsFilter struct{ query, action, namespace, kind string }

// POST creates a DownloadRequest; run access, namespace pin and caller identity
// are enforced by handleVeleroRunArtifact, just as for messages.
func (s *Server) handleVeleroRunContents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := veleroContentsFilter{query: strings.ToLower(q.Get("q")), action: q.Get("action"), namespace: q.Get("namespace"), kind: q.Get("resourceKind")}
	if filter.action != "" && !veleroRestoreAction(filter.action) {
		s.writeError(w, http.StatusBadRequest, "action must be created, updated, skipped or failed")
		return
	}
	s.handleVeleroRunArtifact(w, r, "contents", "ResourceList", maxVeleroCompressedBytes, maxVeleroDecodedBytes, func(reader io.Reader) (any, error) {
		return decodeVeleroContents(reader, strings.EqualFold(chi.URLParam(r, "kind"), "restores"), filter)
	})
}

func veleroRestoreAction(action string) bool {
	switch action {
	case "created", "updated", "skipped", "failed":
		return true
	}
	return false
}

func decodeVeleroContents(reader io.Reader, restore bool, filter veleroContentsFilter) (any, error) {
	out := veleroRunContents{Groups: []veleroContentGroup{}, Namespaces: []string{}, Actions: map[string]int{}}
	namespaces := map[string]bool{}
	dec := json.NewDecoder(reader)
	start, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if start != json.Delim('{') {
		return nil, errNotAResultsFile
	}
	rows := 0
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		kind, ok := key.(string)
		if !ok || !strings.Contains(kind, "/") {
			return nil, errNotAResultsFile
		}
		start, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if start != json.Delim('[') {
			return nil, errNotAResultsFile
		}
		group := veleroContentGroup{Kind: kind, Items: []veleroContentItem{}}
		for dec.More() {
			var raw string
			if err := dec.Decode(&raw); err != nil {
				return nil, err
			}
			item := veleroContentItem{}
			path := raw
			if restore && strings.HasSuffix(path, ")") {
				if at := strings.LastIndex(path, "("); at >= 0 {
					action := path[at+1 : len(path)-1]
					if veleroRestoreAction(action) {
						item.Action = action
						path = strings.TrimSpace(path[:at])
					}
				}
			}
			if ns, name, found := strings.Cut(path, "/"); found {
				item.Namespace, item.Name = ns, name
			} else {
				item.Name = path
			}
			if item.Namespace != "" {
				namespaces[item.Namespace] = true
			}
			if kind == "v1/Namespace" {
				namespaces[item.Name] = true
			}
			group.Count++
			out.Total++
			if item.Action != "" {
				out.Actions[item.Action]++
			}
			if filter.action != "" && filter.action != item.Action {
				continue
			}
			if filter.namespace != "" && filter.namespace != item.Namespace && !(kind == "v1/Namespace" && filter.namespace == item.Name) {
				continue
			}
			if filter.kind != "" && filter.kind != kind {
				continue
			}
			if filter.query != "" && !strings.Contains(strings.ToLower(kind+" "+raw), filter.query) {
				continue
			}

			group.Matched++
			out.Matched++
			if rows < maxVeleroArtifactRows {
				group.Items = append(group.Items, item)
				rows++
			} else {
				out.Truncated = true
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		out.Groups = append(out.Groups, group)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errNotAResultsFile
		}
		return nil, err
	}
	for namespace := range namespaces {
		out.Namespaces = append(out.Namespaces, namespace)
	}
	sort.Strings(out.Namespaces)
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Kind < out.Groups[j].Kind })
	return out, nil
}

type veleroLogLine struct {
	Number int    `json:"number"`
	Level  string `json:"level"`
	Text   string `json:"text"`
}

type veleroRunLog struct {
	Lines        []veleroLogLine `json:"lines"`
	Counts       map[string]int  `json:"counts"`
	Matched      int             `json:"matched"`
	Truncated    bool            `json:"truncated"`
	Partial      bool            `json:"partial"`
	DecodedBytes int64           `json:"decodedBytes"`
	Limit        string          `json:"limit,omitempty"`
}

var veleroLogLevels = []string{"error", "warning", "info", "debug", "other"}

func (s *Server) handleVeleroRunLog(w http.ResponseWriter, r *http.Request) {
	levels := map[string]bool{}
	requested := "error,warning"
	if r.URL.Query().Has("levels") {
		requested = r.URL.Query().Get("levels")
	}
	for _, level := range strings.Split(requested, ",") {
		if !slices.Contains(veleroLogLevels, level) {
			s.writeError(w, http.StatusBadRequest, "levels must be error, warning, info, debug or other")
			return
		}
		levels[level] = true
	}
	s.handleVeleroRunArtifact(w, r, "log", "Log", maxVeleroCompressedBytes, maxVeleroDecodedBytes, func(reader io.Reader) (any, error) {
		return decodeVeleroLog(reader, levels, strings.ToLower(r.URL.Query().Get("q")))
	})
}

// Consume whole logfmt values so level=error inside a quoted message is never
// mistaken for the line's level.
var veleroLogfmtPair = regexp.MustCompile(`(?:^|\s)([^\s=]+)=("(?:\\.|[^"\\])*"|[^\s]*)`)

func veleroLogLevel(line string) string {
	raw := ""
	if strings.HasPrefix(strings.TrimSpace(line), "{") {
		var record struct {
			Level string `json:"level"`
		}
		if json.Unmarshal([]byte(line), &record) == nil {
			raw = record.Level
		}
	} else {
		for _, pair := range veleroLogfmtPair.FindAllStringSubmatch(line, -1) {
			if pair[1] != "level" {
				continue
			}
			raw = pair[2]
			if strings.HasPrefix(raw, `"`) {
				raw, _ = strconv.Unquote(raw)
			}
			break
		}
	}
	switch strings.ToLower(raw) {
	case "error", "fatal", "panic":
		return "error"
	case "warn", "warning":
		return "warning"
	case "info":
		return "info"
	case "debug":
		return "debug"
	default:
		return "other"
	}
}

func decodeVeleroLog(reader io.Reader, levels map[string]bool, query string) (any, error) {
	out := veleroRunLog{Lines: []veleroLogLine{}, Counts: map[string]int{}}
	for _, level := range veleroLogLevels {
		out.Counts[level] = 0
	}
	buf := bufio.NewReaderSize(reader, maxVeleroLogLineBytes+2)
	number := 0
	for {
		line, err := buf.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) || len(bytes.TrimSuffix(line, []byte{'\n'})) > maxVeleroLogLineBytes {
			out.Partial, out.Limit = true, "64 KiB line limit"
			break
		}
		if err != nil && err != io.EOF {
			switch {
			case errors.Is(err, errVeleroCompressedLimit):
				out.Limit = "16 MiB compressed limit"
			case errors.Is(err, errVeleroDecodedLimit):
				out.Limit = "64 MiB decoded limit"
			default:
				return nil, err
			}
			out.Partial = true
			break
		}
		if len(line) > 0 {
			out.DecodedBytes += int64(len(line))
			number++
			text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
			level := veleroLogLevel(text)
			out.Counts[level]++
			if levels[level] && (query == "" || strings.Contains(strings.ToLower(text), query)) {
				out.Matched++
				if len(out.Lines) < maxVeleroArtifactRows {
					out.Lines = append(out.Lines, veleroLogLine{Number: number, Level: level, Text: text})
				} else {
					out.Truncated = true
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	return out, nil
}
