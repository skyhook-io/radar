package substrate

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error codes, shared by the API tier status, per-kind coverage and error
// responses.
const (
	// CodeSharedRadar: a shared Radar without sign-in (a non-loopback
	// listener, or in-cluster with auth off). It cannot tell who is asking,
	// so it refuses API reads and private-local connection edits.
	CodeSharedRadar = "sharedRadar"
	// CodeAPIDisabled: the operator turned Substrate's API off for this
	// installation (RADAR_SUBSTRATE_API=disabled).
	CodeAPIDisabled = "apiDisabled"
	// CodeConflict: the saved connection revision changed, or another process
	// is updating the file.
	CodeConflict = "conflict"
	// CodeConfirmDestination: a save keeps the saved credential but sends it
	// to a changed endpoint, CA or server name, and the caller has not agreed.
	CodeConfirmDestination = "confirmDestination"
	// CodeMintDenied: the signed-in caller's own Kubernetes access cannot
	// create a token for ate-system/ate-client, or Radar has no identity of
	// theirs to create it with.
	CodeMintDenied = "mintDenied"
	// CodeNotConfigured: no identity has been chosen for this context.
	CodeNotConfigured = "notConfigured"
	// CodeTargetChanged: the current Kubernetes target differs from the one the
	// caller reviewed or the one the settings were saved for.
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
	// CodeContextChanged: the Kubernetes context is changing, or a connection
	// change or settings update invalidated the operation. Safe to retry.
	CodeContextChanged = "contextChanged"
	CodeFailed         = "failed"
	// CodeInvalidSettings: invalid connection input or installation settings,
	// including a token file that could not supply a token.
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
	Code     string
	Message  string
	keepConn bool
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
	if st.Code() == codes.Unauthenticated {
		if local := creds.localFailure(st.Message()); local != nil {
			return local.Code, creds.scrub(local.Message)
		}
	}
	msg := creds.scrub(st.Message())
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		// gRPC reports a failed TLS handshake (a wrong CA or server name) as
		// Unavailable; the endpoint answered, so it is the certificate.
		if tlsHandshakeFailed(st.Message()) {
			return CodeTLS, "Radar couldn't verify Substrate's API server: " + msg
		}
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

func tlsHandshakeFailed(msg string) bool {
	return strings.Contains(msg, "authentication handshake failed") || strings.Contains(msg, "x509:") || strings.Contains(msg, "tls: ")
}

// connectionLevel codes mean no read can succeed until the connection or
// identity changes.
func connectionLevel(code string) bool {
	switch code {
	case CodeUnreachable, CodeUnauthenticated, CodeTLS, CodeNotInstalled, CodeNotConfigured, CodeMintDenied, CodeInvalidSettings:
		return true
	}
	return false
}
