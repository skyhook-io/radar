package readplan

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCollectorErrorCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code readreason.Code
	}{
		{apierrors.NewUnauthorized("rejected"), readreason.RadarCredentialsRejected},
		{apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied")), readreason.RadarForbidden},
		{k8score.ErrResourceListTooLarge, readreason.ListTooLarge},
		{k8score.ErrCollectionNotSupported, readreason.KindNotWatchable},
		{k8s.ErrUnknownDynamicKind, readreason.KindUnknown},
		{k8s.ErrDynamicNotReady, readreason.KindSyncPending},
		{context.DeadlineExceeded, ""},
		{context.Canceled, ""},
		{apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "missing"), ""},
		{errors.New("other"), ""},
		{nil, ""},
	} {
		for _, err := range []error{tc.err, fmt.Errorf("wrapped: %w", tc.err)} {
			code, ok := CollectorErrorCode(err)
			if code != tc.code || ok != (tc.code != "") {
				t.Errorf("%v = %s / %t, want %s", err, code, ok, tc.code)
			}
		}
	}
}
