package images

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/google/go-containerregistry/pkg/authn"
)

const ecrRegistriesEnv = "RADAR_JAVA4SHELL_ECR_REGISTRIES"

var ecrRegistryPattern = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr\.([a-z][a-z0-9-]+-[0-9]+)\.amazonaws\.com(?:\.cn)?$`)

var errECRCredentials = errors.New("Radar could not obtain credentials for this Amazon ECR registry")

var runtimeECRKeychain = sync.OnceValue(func() *ecrKeychain {
	return newECRKeychain(os.Getenv(ecrRegistriesEnv), requestECRToken)
})

type ecrTokenRequest func(ctx context.Context, registry, account, region string) (authn.Authenticator, time.Time, error)

type ecrKeychain struct {
	registries map[string]*ecrRegistryAuth
	request    ecrTokenRequest
}

type ecrFallbackKeychain struct {
	ecr      *ecrKeychain
	fallback authn.Keychain
}

func (k *ecrFallbackKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	ctx, cancel := context.WithTimeout(context.Background(), configFetchTimeout)
	defer cancel()
	return k.ResolveContext(ctx, resource)
}

func (k *ecrFallbackKeychain) ResolveContext(ctx context.Context, resource authn.Resource) (authn.Authenticator, error) {
	auth, err := k.ecr.ResolveContext(ctx, resource)
	if err != nil && ctx.Err() == nil {
		if fallback, fallbackErr := authn.Resolve(ctx, k.fallback, resource); fallbackErr == nil && fallback != authn.Anonymous {
			return fallback, nil
		}
	}
	return auth, err
}

type ecrRegistryAuth struct {
	account string
	region  string
	lock    chan struct{}
	auth    authn.Authenticator
	expires time.Time
	retryAt time.Time
}

func newECRKeychain(registries string, request ecrTokenRequest) *ecrKeychain {
	k := &ecrKeychain{registries: map[string]*ecrRegistryAuth{}, request: request}
	for _, registry := range strings.Split(registries, ",") {
		registry = strings.TrimSpace(registry)
		if parts := ecrRegistryPattern.FindStringSubmatch(registry); parts != nil {
			k.registries[registry] = &ecrRegistryAuth{
				account: parts[1], region: parts[2], lock: make(chan struct{}, 1),
			}
		}
	}
	return k
}

func (k *ecrKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	ctx, cancel := context.WithTimeout(context.Background(), configFetchTimeout)
	defer cancel()
	return k.ResolveContext(ctx, resource)
}

func (k *ecrKeychain) ResolveContext(ctx context.Context, resource authn.Resource) (authn.Authenticator, error) {
	host := resource.RegistryStr()
	entry := k.registries[host]
	if entry == nil {
		return authn.Anonymous, nil
	}
	select {
	case entry.lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-entry.lock }()
	now := time.Now()
	if now.Before(entry.expires) {
		return entry.auth, nil
	}
	if now.Before(entry.retryAt) {
		return nil, errECRCredentials
	}
	auth, expiry, err := k.request(ctx, host, entry.account, entry.region)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		entry.retryAt = time.Now().Add(15 * time.Second)
		return nil, errECRCredentials
	}
	entry.auth = auth
	entry.expires = expiry.Add(-time.Minute)
	return auth, nil
}

func requestECRToken(ctx context.Context, registry, account, region string) (authn.Authenticator, time.Time, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region),
		config.WithEC2IMDSClientEnableState(imds.ClientDisabled), config.WithRetryMaxAttempts(2))
	if err != nil {
		return nil, time.Time{}, err
	}
	output, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{
		RegistryIds: []string{account},
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	for _, data := range output.AuthorizationData {
		if aws.ToString(data.ProxyEndpoint) != "https://"+registry || !time.Now().Before(aws.ToTime(data.ExpiresAt)) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(aws.ToString(data.AuthorizationToken))
		if err != nil {
			continue
		}
		username, password, ok := strings.Cut(string(raw), ":")
		if ok && username == "AWS" && password != "" {
			return &authn.Basic{Username: username, Password: password}, aws.ToTime(data.ExpiresAt), nil
		}
	}
	return nil, time.Time{}, errECRCredentials
}
