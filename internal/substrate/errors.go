package substrate

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error codes, shared by the API tier status, per-kind coverage and error
// responses.
const (
	// CodeSharedRadar: this Radar is shared (auth on, a non-loopback
	// listener, in-cluster or Cloud), so it does not read Substrate's API.
	CodeSharedRadar = "sharedRadar"
	// CodeNotConfigured: no identity has been chosen for this context.
	CodeNotConfigured = "notConfigured"
	// CodeTargetChanged: the context now points at another cluster than the
	// one the settings were saved for.
	CodeTargetChanged = "targetChanged"
	// CodeNotInstalled: Service ate-system/api does not exist.
	CodeNotInstalled    = "notInstalled"
	CodeTLS             = "tls"
	CodeUnreachable     = "unreachable"
	CodeUnauthenticated = "unauthenticated"
	CodeDenied          = "denied"
	CodeNotFound        = "notFound"
	CodeCapExceeded     = "capExceeded"
	// CodeScoped: Substrate would not list atespaces, so Radar read only the
	// atespaces named in its settings; the rest are unknown.
	CodeScoped = "scoped"
	// CodeContextChanged: the cluster connection or the settings changed
	// while the read ran, so its result was discarded. Safe to retry.
	CodeContextChanged = "contextChanged"
	CodeFailed         = "failed"
	// CodeInvalidSettings: the submitted connection settings are invalid.
	CodeInvalidSettings = "invalidSettings"
	// CodeSettingsFile: Radar could not read or write ~/.radar/substrate.json.
	CodeSettingsFile = "settingsFile"
	// CodeLogsUnavailable: the worker Pod's log could not be read (the
	// logs endpoint only).
	CodeLogsUnavailable = "logsUnavailable"
	// CodeIncomplete: Radar read too little to say whether the object exists
	// (pool detail, when the WorkerPool read and the worker list are both
	// incomplete).
	CodeIncomplete = "incomplete"
)

// TierError is a failure of the whole API tier: nothing could be read.
type TierError struct {
	Code    string
	Message string
}

func (e *TierError) Error() string { return e.Message }

// classify maps a gRPC error to a code and a message that never carries the
// token.
func classify(err error, creds *bearerCreds) (string, string) {
	var tier *TierError
	if errors.As(err, &tier) {
		return tier.Code, creds.scrub(tier.Message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeUnreachable, "Substrate's API did not answer in time"
	}
	st, ok := status.FromError(err)
	if !ok {
		return CodeFailed, creds.scrub(err.Error())
	}
	msg := creds.scrub(st.Message())
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return CodeUnreachable, "Substrate's API is unreachable: " + msg
	case codes.Unauthenticated:
		return CodeUnauthenticated, "Substrate did not accept Radar's identity: " + msg
	case codes.PermissionDenied:
		return CodeDenied, "Substrate denied the read: " + msg
	case codes.NotFound:
		return CodeNotFound, msg
	}
	return CodeFailed, "Substrate's API failed the read: " + msg
}

// connectionLevel codes mean no read can succeed until the connection or
// identity changes.
func connectionLevel(code string) bool {
	switch code {
	case CodeUnreachable, CodeUnauthenticated, CodeTLS, CodeNotInstalled, CodeNotConfigured:
		return true
	}
	return false
}
