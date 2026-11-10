package cloud

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
)

// ClusterFacts is the best-effort description sent with an enroll request.
type ClusterFacts struct {
	K8sVersion string
	Distro     string
	NodeCount  *int
}

// EnrollmentConfig runs a Radar that joins a Hub with a shared enrollment key.
// The token the Hub hands back lives in memory only: every start joins again
// and gets a new one.
type EnrollmentConfig struct {
	HubOrigin string
	KeyFile   string
	CA        *x509.CertPool
	Client    kubernetes.Interface
	Namespace string // where Radar runs
	Release   string
	Names     NameOptions
	// Facts is called before each enroll call; nil sends none.
	Facts func(context.Context) ClusterFacts

	// Tunnel is the template for the tunnel once joined: Handler,
	// SelfUpgradeAvailable, APIServerURL and the like. URL, Token, ClusterID,
	// ClusterName, CA, Enrolled and OnConnected are filled in here.
	Tunnel Config
}

func (c EnrollmentConfig) validate() error {
	if _, err := NormalizeHubOrigin(c.HubOrigin); err != nil {
		return fmt.Errorf("cloud: Hub URL: %w", err)
	}
	switch {
	case c.KeyFile == "":
		return errors.New("cloud: enrollment key file is required")
	case c.Client == nil:
		return errors.New("cloud: Kubernetes client is required")
	case c.Namespace == "":
		return errors.New("cloud: Radar's namespace is required (MY_POD_NAMESPACE)")
	case c.Release == "":
		return errors.New("cloud: the Helm release name is required (RADAR_HELM_RELEASE)")
	case c.Tunnel.Handler == nil:
		return errors.New("cloud: Handler is required")
	}
	return c.Names.Validate()
}

// Delays between enroll attempts.
var (
	enrollMinBackoff = 30 * time.Second
	enrollMaxBackoff = 10 * time.Minute
	// Refusals that need someone to act in the Hub (plan or license limit,
	// a removed cluster, a full key) wait the long delay straight
	// away, so an owner's fix is picked up with no restart.
	enrollLongDelay = 10 * time.Minute
	// A Hub with no org yet (503) is ready the moment its admin first signs
	// in, so it is retried often and never backs off further.
	enrollNotReadyDelay = 30 * time.Second
	// Another Radar with this identity holds the Hub's entry. The Hub frees
	// it within about 90 seconds of that Radar dying, so this polls often.
	enrollConnectedDelay   = 15 * time.Second
	enrollDefaultRateDelay = time.Minute
	// A Retry-After longer than this is cut to it, so a proxy in the way
	// cannot keep Radar from joining for hours.
	enrollMaxRetryAfter = 10 * time.Minute
	// A refusal repeated with the same reason is logged again after this.
	enrollRelogAfter = 10 * time.Minute
)

type enroller struct {
	cfg        EnrollmentConfig
	client     *EnrollClient
	backoff    time.Duration
	uid        string
	name       string // the name Radar resolved and sends
	source     string
	lastReason string
	lastLogAt  time.Time
	runTunnel  func(context.Context, Config) error
}

// RunEnrollment joins the Hub, then serves the tunnel with the token it got.
// When the Hub stops accepting that token it joins again. It returns only
// when ctx ends or the configuration can never work.
func RunEnrollment(ctx context.Context, cfg EnrollmentConfig) error {
	e, err := newEnroller(cfg)
	if err != nil {
		return err
	}
	return e.run(ctx)
}

func newEnroller(cfg EnrollmentConfig) (*enroller, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	origin, _ := NormalizeHubOrigin(cfg.HubOrigin)
	origin = enrollmentHubOrigin(origin)
	cfg.HubOrigin = origin
	client, err := NewEnrollClient(origin, cfg.KeyFile, cfg.CA)
	if err != nil {
		return nil, err
	}
	return &enroller{
		cfg:       cfg,
		client:    client,
		runTunnel: Run,
	}, nil
}

func (e *enroller) run(ctx context.Context) error {
	log.Printf("[cloud] joining %s", e.cfg.HubOrigin)
	for ctx.Err() == nil {
		res, delay := e.attempt(ctx)
		if res == nil {
			e.wait(ctx, delay)
			continue
		}
		connected, err := e.serveTunnel(ctx, res)
		if ctx.Err() != nil {
			break
		}
		if connected {
			e.backoff = 0
		}
		// A token the Hub stopped accepting after it worked (a restart
		// elsewhere, a rotation): join again at once. One refused before the
		// tunnel ever came up waits, so a Hub that refuses every new token
		// can't be hammered.
		if connected && errors.Is(err, ErrTokenRejected) {
			log.Printf("[cloud] The Hub no longer accepts this cluster's token. Joining again now.")
			continue
		}
		delay = e.nextBackoff()
		wait := delay.Round(time.Second)
		if errors.Is(err, ErrTokenRejected) {
			log.Printf("[cloud] The Hub no longer accepts this cluster's token. Joining again in %s.", wait)
		} else {
			log.Printf("[cloud] tunnel stopped: %v. Joining again in %s.", err, wait)
		}
		e.wait(ctx, delay)
	}
	return ctx.Err()
}

// attempt makes one enroll call. On a refusal it returns how long to wait
// before the next one.
func (e *enroller) attempt(ctx context.Context) (*EnrollResult, time.Duration) {
	if e.uid == "" {
		uidCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		uid, err := DiscoverClusterUID(uidCtx, e.cfg.Client)
		cancel()
		var apiURL string
		if err == nil {
			// The default name comes from this URL: retry a failed read
			// rather than join under a fallback name.
			apiCtx, cancelAPI := context.WithTimeout(ctx, 10*time.Second)
			apiURL, err = LookupAPIServerURL(apiCtx, e.cfg.Client)
			cancelAPI()
		}
		if err != nil {
			delay := e.nextBackoff()
			reason := fmt.Sprintf("Cannot join yet: %v.", err)
			e.logRefusal(reason, retryPhrase(delay, false))
			return nil, delay
		}
		e.uid = uid
		e.cfg.Tunnel.APIServerURL = apiURL
		e.name, e.source = ResolveClusterName(e.cfg.Names, apiURL, uid)
	}

	req := EnrollRequest{
		AgentUID:     e.uid,
		Namespace:    e.cfg.Namespace,
		Release:      e.cfg.Release,
		Name:         e.name,
		NameSource:   e.source,
		RadarVersion: truncateRunes(Version, 64),
	}
	if apiURL, err := validateAPIServerURL(e.cfg.Tunnel.APIServerURL); err == nil {
		req.APIServerURL = apiURL
	}
	if e.cfg.Facts != nil {
		facts := e.cfg.Facts(ctx)
		req.K8sVersion = truncateRunes(facts.K8sVersion, 64)
		req.Distro = truncateRunes(facts.Distro, 64)
		req.NodeCount = facts.NodeCount
	}

	res, err := e.client.Enroll(ctx, req)
	if ctx.Err() != nil {
		return nil, 0
	}
	if err == nil {
		e.lastReason = ""
		log.Printf("[cloud] joined %s as cluster %s (%q)", e.cfg.HubOrigin, res.ClusterID, e.name)
		return res, 0
	}

	out := classifyEnrollError(err)
	delay := out.delay
	if delay == 0 {
		delay = e.nextBackoff()
	}
	e.logRefusal(out.reason, retryPhrase(delay, out.long))
	return nil, delay
}

// enrollOutcome is how Radar reacts to a failed enroll call.
type enrollOutcome struct {
	reason string // one or two sentences for the log
	// delay before the next attempt; 0 means the regular backoff.
	delay time.Duration
	// long marks the fixed ten-minute wait for refusals an owner must fix.
	long bool
}

// classifyEnrollError maps a failed enroll call to an outcome. The Hub's own
// message, when it sends one, replaces Radar's default reason.
func classifyEnrollError(err error) enrollOutcome {
	var he *EnrollError
	if !errors.As(err, &he) {
		if untrustedCert(err) {
			return enrollOutcome{reason: fmt.Sprintf("Radar does not trust the Hub's certificate. %s Details: %v.", caBundleFix, err)}
		}
		return enrollOutcome{reason: fmt.Sprintf("Join failed: %v.", err)}
	}
	reason := func(def string) string {
		if he.Message != "" {
			return he.Message
		}
		return def
	}
	long := func(def string) enrollOutcome {
		return enrollOutcome{reason: reason(def), delay: jitter(enrollLongDelay), long: true}
	}
	switch {
	case he.StatusCode == http.StatusUnauthorized:
		return enrollOutcome{reason: reason("The Hub rejected the enrollment key. Put a current key in its Secret, then restart Radar.")}
	case he.StatusCode == http.StatusPaymentRequired:
		return long("The Hub refused to add this cluster: its license or plan limit is reached.")
	case he.StatusCode == http.StatusForbidden && he.Code == "cluster_removed":
		return long("This cluster was removed in Radar Hub. An owner can let it back in under Settings > Enrollment keys.")
	case he.StatusCode == http.StatusForbidden && he.Code == "pending_approval":
		return enrollOutcome{reason: reason("This cluster is waiting for an owner to approve it in Radar Hub."), delay: jitter(enrollNotReadyDelay)}
	case he.StatusCode == http.StatusConflict && he.Code == "key_full":
		return long("This enrollment key has reached its cluster limit. An owner can raise it on the Enrollment keys page in Radar Hub.")
	case he.StatusCode == http.StatusConflict && he.Code == "already_connected":
		return enrollOutcome{reason: reason("Another Radar with this cluster's identity is connected to the Hub."), delay: jitter(enrollConnectedDelay)}
	case he.StatusCode == http.StatusConflict && he.Code == "no_agent_address":
		return enrollOutcome{reason: reason("The Hub has no agent address configured.")}
	case he.StatusCode == http.StatusBadRequest:
		return enrollOutcome{reason: reason("The Hub could not read the join request.")}
	case he.StatusCode == http.StatusServiceUnavailable:
		return enrollOutcome{reason: reason("The Hub is not ready to add clusters yet."), delay: jitter(enrollNotReadyDelay)}
	case he.StatusCode == http.StatusTooManyRequests:
		d := enrollDefaultRateDelay
		if he.HasRetryAfter {
			d = min(max(he.RetryAfter, time.Second), enrollMaxRetryAfter)
		}
		return enrollOutcome{reason: reason("The Hub is rate limiting join requests."), delay: d}
	default:
		return enrollOutcome{reason: reason(he.Error() + ".")}
	}
}

const caBundleFix = "If the Hub uses a self-signed or private certificate, give Radar its CA with the chart value cloud.caBundle (or --cloud-ca-file)."

// untrustedCert reports a Hub certificate no trusted CA signed, which
// caBundleFix fixes.
func untrustedCert(err error) bool {
	var unknown x509.UnknownAuthorityError
	return errors.As(err, &unknown)
}

func retryPhrase(delay time.Duration, long bool) string {
	if long {
		return "Checking again every 10 minutes."
	}
	return fmt.Sprintf("Trying again in %s.", delay.Round(time.Second))
}

// logRefusal logs a refusal when its reason changes, and repeats an unchanged
// one only every enrollRelogAfter, so a cluster that waits for hours does not
// flood the log.
func (e *enroller) logRefusal(reason, retry string) {
	now := time.Now()
	if reason == e.lastReason && now.Sub(e.lastLogAt) < enrollRelogAfter {
		return
	}
	e.lastReason, e.lastLogAt = reason, now
	log.Printf("[cloud] %s %s", reason, retry)
}

// serveTunnel runs the tunnel with res's token until ctx ends or the tunnel
// stops for good, typically because the Hub no longer accepts the token. It
// reports whether a session was ever open.
func (e *enroller) serveTunnel(ctx context.Context, res *EnrollResult) (bool, error) {
	tunnel := e.cfg.Tunnel
	tunnel.URL = res.AgentURL
	tunnel.Token = res.Token
	tunnel.ClusterID = res.ClusterID
	tunnel.ClusterName = e.name
	tunnel.CA = e.cfg.CA
	tunnel.Enrolled = true
	var connected atomic.Bool
	tunnel.OnConnected = func() { connected.Store(true) }
	err := e.runTunnel(ctx, tunnel)
	return connected.Load(), err
}

func (e *enroller) wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (e *enroller) nextBackoff() time.Duration {
	if e.backoff == 0 {
		e.backoff = enrollMinBackoff
	} else {
		e.backoff = min(e.backoff*2, enrollMaxBackoff)
	}
	return jitter(e.backoff)
}

// jitter spreads d over [d, 1.2d) so a fleet started together does not
// retry in lockstep.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}
