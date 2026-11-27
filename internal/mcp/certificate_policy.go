package mcp

import (
	"context"
	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/issues"
)

func newIssueProvider(ctx context.Context) *issues.CacheProvider {
	return issues.NewCacheProvider(audit.CertificateExpiryPolicyFromContext(ctx))
}
