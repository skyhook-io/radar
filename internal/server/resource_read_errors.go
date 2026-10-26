package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// readStatus is the HTTP status of a failed resource read, by code. It is
// transport only — clients branch on error_code — but it is chosen so a UI or
// fleet client that branches on status alone still does the right thing:
// Radar's own collection is never 401/403 (that reads as "you lack access"),
// and a configuration limit is never 503 (that reads as "retry"). Fleet
// clients exclude 503 from their cluster circuit breaker, so a collector
// failure scoped to one kind is 503 rather than another 5xx.
var readStatus = map[readreason.Code]int{
	readreason.UserForbidden:              http.StatusForbidden,
	readreason.UserNoNamespaceAccess:      http.StatusForbidden,
	readreason.UserNamespaceNotVisible:    http.StatusForbidden,
	readreason.UserPermissionCheckFailed:  http.StatusServiceUnavailable,
	readreason.RadarForbidden:             http.StatusServiceUnavailable,
	readreason.RadarCredentialsRejected:   http.StatusServiceUnavailable,
	readreason.RadarNamespaceNotCollected: http.StatusConflict,
	readreason.KindSyncPending:            http.StatusServiceUnavailable,
	readreason.KindSyncTimedOut:           http.StatusServiceUnavailable,
	readreason.RadarKindNotCollected:      http.StatusServiceUnavailable,
	readreason.KindUnknown:                http.StatusBadRequest,
	readreason.KindNotWatchable:           http.StatusBadRequest,
	readreason.KindNotSearchable:          http.StatusBadRequest,
	readreason.OutsideNamespacePin:        http.StatusConflict,
	readreason.NamespaceNotFound:          http.StatusNotFound,
	readreason.NamespaceRequired:          http.StatusBadRequest,
	readreason.ObjectNotFound:             http.StatusNotFound,
	readreason.ListTooLarge:               http.StatusBadRequest,
	readreason.FilterEvalFailed:           http.StatusBadRequest,
	readreason.FindingsScanIncomplete:     http.StatusBadRequest,
	readreason.ClusterConnecting:          http.StatusServiceUnavailable,
	readreason.ClusterDisconnected:        http.StatusServiceUnavailable,
	readreason.ConnectionChanged:          http.StatusConflict,
	readreason.ReadError:                  http.StatusInternalServerError,
}

func readStatusFor(code readreason.Code) int {
	if status, ok := readStatus[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// resourceReadFailure is a read that served nothing. gaps keeps every reason
// when namespaces failed for different ones; code is the most actionable.
type resourceReadFailure struct {
	code         readreason.Code
	message      string
	gaps         []resourceapi.Gap
	count, limit int
}

func (e *resourceReadFailure) Error() string { return e.message }

func readFailuref(code readreason.Code, format string, args ...any) *resourceReadFailure {
	return &resourceReadFailure{code: code, message: fmt.Sprintf(format, args...)}
}

// permissionFailure is a caller permission check that didn't allow the read:
// a denial, or — when the check itself failed — a retryable non-answer.
func permissionFailure(checked bool, denial string) *resourceReadFailure {
	if !checked {
		return readFailuref(readreason.UserPermissionCheckFailed, "Radar could not complete your permission check; retry when it is available")
	}
	return readFailuref(readreason.UserForbidden, "%s", denial)
}

// errReadHandlesChanged marks a read whose cache, dynamic cache or discovery
// was replaced while it ran, so its rows may mix two clusters.
var errReadHandlesChanged = errors.New("Radar's cluster connection changed during this read; confirm which cluster Radar is connected to, then retry")

func collectionFailure(resource string, err error) *resourceReadFailure {
	code, _ := readplan.CollectorErrorCode(err)
	switch {
	case errors.Is(err, errReadHandlesChanged):
		return readFailuref(readreason.ConnectionChanged, "%s", err.Error())
	case code == readreason.ListTooLarge:
		return readFailuref(code, "Too many resources to load; select fewer namespaces")
	case code == readreason.KindNotWatchable:
		return readFailuref(code, "%s cannot be collected with list/watch", resource)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return readFailuref(readreason.ReadError, "Kubernetes read did not finish within the request budget; retry with fewer namespaces")
	case code == readreason.RadarCredentialsRejected:
		return readFailuref(code, "Kubernetes rejected Radar's collector credentials")
	case code == readreason.RadarForbidden:
		return readFailuref(code, "Radar's collector cannot read %s in the requested scope", resource)
	case code == readreason.KindUnknown:
		return readFailuref(code, "%s", err.Error())
	case code == readreason.KindSyncPending:
		return readFailuref(code, "Resource discovery is still loading")
	default:
		return readFailuref(readreason.ReadError, "Radar could not read %s", resource)
	}
}

// writeReadFailure answers a failed read with {error, error_code, coverage?}.
// target, when the read has one, gives the failure its coverage: the scope
// and every gap, with no read scope because Radar read nothing.
func (s *Server) writeReadFailure(w http.ResponseWriter, resource string, err error, target *listTarget) {
	var failure *resourceReadFailure
	if !errors.As(err, &failure) {
		failure = collectionFailure(resource, err)
	}
	if failure.code == readreason.ReadError {
		log.Printf("[resource-read] Failed to read %s: %v", resource, err)
	}
	body := resourceapi.ReadError{Error: failure.message, ErrorCode: failure.code}
	if target != nil {
		gaps := append(slices.Clone(target.gaps), failure.gaps...)
		if !slices.ContainsFunc(gaps, func(gap resourceapi.Gap) bool { return gap.Code == failure.code }) {
			gaps = append(gaps, resourceapi.Gap{Code: failure.code})
		}
		body.Coverage = &resourceapi.Coverage{Status: resourceapi.CoveragePartial, Scope: target.scope(), NarrowedBy: target.narrowedBy, Gaps: target.placeGaps(gaps, nil)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(readStatusFor(failure.code))
	s.writeJSON(w, struct {
		resourceapi.ReadError
		Count int `json:"count,omitempty"`
		Limit int `json:"limit,omitempty"`
	}{ReadError: body, Count: failure.count, Limit: failure.limit})
}

// writeCollectionFailure answers a failed read that has no list target.
func (s *Server) writeCollectionFailure(w http.ResponseWriter, resource string, err error) {
	s.writeReadFailure(w, resource, err, nil)
}

// noAccessFailure says why a list the caller can read nothing of failed: the
// namespaces it named are outside the pin or hidden, the caller's namespace
// access couldn't be determined, or they have none.
func noAccessFailure(failure *resourceReadFailure, target listTarget, accessCheckFailed bool) *resourceReadFailure {
	if failure.code != readreason.UserNoNamespaceAccess {
		return failure
	}
	if accessCheckFailed {
		return readFailuref(readreason.UserPermissionCheckFailed, "Radar could not determine your namespace access; retry when the permission check is available")
	}
	if len(target.gaps) > 0 {
		codes := make([]readreason.Code, 0, len(target.gaps))
		for _, gap := range target.gaps {
			codes = append(codes, gap.Code)
		}
		primary := readreason.Primary(codes)
		return &resourceReadFailure{code: primary, message: namespaceGapMessage(primary, target.gaps, failure.message)}
	}
	return failure
}

// namespaceGapMessage words a list failure after the namespaces of its primary
// gap, as a get of one of them would. They are names the caller asked for, so
// naming them reveals nothing.
func namespaceGapMessage(code readreason.Code, gaps []resourceapi.Gap, fallback string) string {
	var names []string
	for _, gap := range gaps {
		if gap.Code == code && gap.Namespaces != nil {
			names = append(names, gap.Namespaces.Names...)
		}
	}
	if len(names) == 0 {
		return fallback
	}
	quoted := make([]string, len(names))
	for i, ns := range names {
		quoted[i] = strconv.Quote(ns)
	}
	list := strings.Join(quoted, ", ")
	one := len(names) == 1
	noun := "namespaces " + list
	if one {
		noun = "namespace " + list
	}
	switch code {
	case readreason.NamespaceNotFound:
		if one {
			return noun + " doesn't exist in this cluster"
		}
		return noun + " don't exist in this cluster"
	case readreason.UserNamespaceNotVisible:
		if one {
			return noun + " is not among the namespaces you can see, or does not exist"
		}
		return noun + " are not among the namespaces you can see, or do not exist"
	case readreason.OutsideNamespacePin:
		if one {
			return noun + " is outside Radar's --namespace-scope pin"
		}
		return noun + " are outside Radar's --namespace-scope pin"
	}
	return fallback
}

func dynamicPlanFailure(resource string, code readreason.Code) *resourceReadFailure {
	switch code {
	case readreason.ConnectionChanged:
		return readFailuref(code, "Radar's cluster connection changed during this read, so the result was discarded; confirm which cluster Radar is connected to, then retry")
	case readreason.KindSyncTimedOut:
		return readFailuref(code, "%s have not finished loading; Radar keeps trying, so retry later", resource)
	case readreason.RadarCredentialsRejected:
		return readFailuref(code, "Kubernetes rejected Radar's own credentials when it checked access to %s", resource)
	case readreason.RadarForbidden:
		return readFailuref(code, "Radar's collector cannot read %s in the requested scope", resource)
	case readreason.RadarNamespaceNotCollected:
		return readFailuref(code, "Radar's collector does not cover the requested scope for %s", resource)
	case readreason.KindNotWatchable:
		return readFailuref(code, "%s cannot be collected with list/watch", resource)
	case readreason.ReadError:
		return readFailuref(code, "Radar's access check for %s failed, so it can't say what exists there; retry", resource)
	case readreason.RadarKindNotCollected:
		return readFailuref(code, "Radar is not watching %s", resource)
	default:
		return readFailuref(code, "%s are still loading, please retry shortly", resource)
	}
}
