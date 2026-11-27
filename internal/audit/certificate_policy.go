package audit

import (
	"context"
	"github.com/skyhook-io/radar/pkg/health"
)

type certificatePolicyKey struct{}

func WithCertificateExpiryPolicy(ctx context.Context, policy health.CertificateExpiryPolicy) context.Context {
	return context.WithValue(ctx, certificatePolicyKey{}, policy)
}

func CertificateExpiryPolicyFromContext(ctx context.Context) health.CertificateExpiryPolicy {
	if ctx == nil {
		return health.CertificateExpiryPolicy{}
	}
	policy, _ := ctx.Value(certificatePolicyKey{}).(health.CertificateExpiryPolicy)
	return policy
}
