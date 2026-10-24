package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	k8svalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/labels"
)

type cloudStartupMode uint8

const (
	cloudOff cloudStartupMode = iota
	// cloudTokenMode: a token configured by hand, with --cluster-name used
	// as the cluster ID.
	cloudTokenMode
	// cloudEnrollMode: join the Hub with a shared enrollment key and keep
	// the cluster's own token in a Secret.
	cloudEnrollMode
)

type cloudStartupFlags struct {
	CloudURL           string
	Token              string
	ClusterName        string
	InsecureSkipVerify bool
	HubURL             string
	EnrollmentKeyFile  string
	TokenSecret        string
	CAFile             string
	NamePrefix         string
	NameSuffix         string
	// From the chart's downward-API env.
	PodNamespace string
	HelmRelease  string
}

type cloudStartup struct {
	mode      cloudStartupMode
	hubOrigin string
	ca        *cloud.CAFile
	names     cloud.NameOptions
	warnings  []string
}

// resolveCloudStartup decides how this process connects to a Hub, before
// anything starts, so a configuration that can never work fails at once.
func resolveCloudStartup(f cloudStartupFlags) (cloudStartup, error) {
	var out cloudStartup
	switch {
	case f.Token != "":
		if f.EnrollmentKeyFile != "" {
			out.warnings = append(out.warnings, "--cloud-token is set, so --cloud-enrollment-key-file is ignored")
		}
		if f.CloudURL == "" {
			if f.EnrollmentKeyFile == "" {
				return out, nil
			}
			return out, errors.New("--cloud-token needs --cloud-url. To join with the enrollment key instead, unset --cloud-token (RADAR_CLOUD_TOKEN)")
		}
		if f.ClusterName == "" {
			return out, errors.New("--cloud-url requires --cloud-token and --cluster-name")
		}
		out.mode = cloudTokenMode
	case f.EnrollmentKeyFile != "":
		origin, err := cloud.NormalizeHubOrigin(strings.TrimSpace(f.HubURL))
		if err != nil {
			if f.HubURL == "" {
				return out, errors.New("--cloud-enrollment-key-file needs --cloud-hub-url, the Hub's https:// address")
			}
			return out, fmt.Errorf("--cloud-hub-url: %w", err)
		}
		if f.InsecureSkipVerify {
			return out, errors.New("--cloud-insecure-skip-verify cannot be used with an enrollment key: the key would be sent to an unverified server. Pass the Hub's CA with --cloud-ca-file instead")
		}
		if f.TokenSecret == "" {
			return out, errors.New("--cloud-enrollment-key-file needs --cloud-token-secret, the Secret Radar keeps its token in")
		}
		if errs := k8svalidation.NameIsDNSSubdomain(f.TokenSecret, false); len(errs) > 0 {
			return out, fmt.Errorf("--cloud-token-secret %q is not a valid Secret name: %s", f.TokenSecret, strings.Join(errs, "; "))
		}
		if f.PodNamespace == "" || f.HelmRelease == "" {
			return out, errors.New("joining with an enrollment key needs MY_POD_NAMESPACE and RADAR_HELM_RELEASE, which the Radar Helm chart sets")
		}
		out.names = cloud.NameOptions{Exact: f.ClusterName, Prefix: f.NamePrefix, Suffix: f.NameSuffix}
		if err := out.names.Validate(); err != nil {
			return out, err
		}
		if f.CloudURL != "" {
			out.warnings = append(out.warnings, "--cloud-url is ignored with an enrollment key: the Hub sends the agent address when the cluster joins")
		}
		out.hubOrigin = origin
		out.mode = cloudEnrollMode
	case f.CloudURL != "":
		return out, errors.New("--cloud-url requires --cloud-token and --cluster-name")
	default:
		return out, nil
	}

	if f.CAFile != "" {
		ca, err := cloud.NewCAFile(f.CAFile)
		if err != nil {
			return out, fmt.Errorf("--cloud-ca-file: %w", err)
		}
		out.ca = ca
	}
	return out, nil
}

// enrollmentFacts describes the cluster for the Hub's cluster list. Every
// field is best effort.
func enrollmentFacts(ctx context.Context) cloud.ClusterFacts {
	var facts cloud.ClusterFacts
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if client := k8s.GetClient(); client != nil {
		if v, err := client.Discovery().ServerVersion(); err == nil && v != nil {
			facts.K8sVersion = v.GitVersion
		}
	}
	if platform, err := k8s.GetClusterPlatform(ctx); err == nil {
		facts.Distro = platform
	}
	if c := k8s.GetResourceCache(); c != nil {
		if lister := c.Nodes(); lister != nil {
			if nodes, err := lister.List(labels.Everything()); err == nil {
				n := len(nodes)
				facts.NodeCount = &n
			}
		}
	}
	return facts
}
