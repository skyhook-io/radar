package cloud

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// ResetEnrollmentHint is the instruction stored in enroll_error whenever
// Radar stops and needs an operator to clear its token Secret.
const ResetEnrollmentHint = "run radar cloud reset-enrollment to join again"

// ClusterFacts is the best-effort description sent with an enroll request.
type ClusterFacts struct {
	K8sVersion string
	Distro     string
	NodeCount  *int
}

// EnrollmentConfig runs a Radar that joins a Hub with a shared enrollment key
// and keeps its own token in a Secret.
type EnrollmentConfig struct {
	HubOrigin  string
	KeyFile    string
	CA         *CAFile
	Client     kubernetes.Interface
	Namespace  string // where Radar and its token Secret live
	Release    string
	SecretName string
	Names      NameOptions
	// Facts is called before each enroll call; nil sends none.
	Facts func(context.Context) ClusterFacts

	// Tunnel is the template for the tunnel once joined: Handler,
	// SelfUpgradeAvailable, APIServerURL and the like. URL, Token, ClusterID,
	// ClusterName, ClusterUID, CA and Enrolled are filled in here.
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
	case c.SecretName == "":
		return errors.New("cloud: token Secret name is required")
	case c.Tunnel.Handler == nil:
		return errors.New("cloud: Handler is required")
	}
	return c.Names.Validate()
}

// Delays between enroll attempts.
var (
	enrollMinBackoff = 30 * time.Second
	enrollMaxBackoff = 10 * time.Minute
	// A Hub-side licence or plan limit (402) doesn't clear in seconds, so it
	// waits the long delay straight away.
	enrollLongDelay = 10 * time.Minute
	// A Hub with no org yet (503) is ready the moment its admin first signs
	// in, so it is retried often and never backs off further: the call is
	// cheap and well inside the Hub's per-IP rate limit.
	enrollNotReadyDelay    = 30 * time.Second
	enrollDefaultRateDelay = time.Minute
	secretRetryDelay       = 30 * time.Second
)

type enroller struct {
	cfg       EnrollmentConfig
	store     *TokenSecretStore
	client    *EnrollClient
	changes   chan struct{}
	backoff   time.Duration
	uid       string
	name      string
	source    string
	runTunnel func(context.Context, Config) error
}

// RunEnrollment joins the Hub if needed, then serves the tunnel with the
// stored token. It watches the token Secret: clearing its data stops the
// tunnel and starts a new join, with no restart. It returns only when ctx
// ends or the configuration can never work.
func RunEnrollment(ctx context.Context, cfg EnrollmentConfig) error {
	e, err := newEnroller(cfg)
	if err != nil {
		return err
	}
	watchTokenSecret(ctx, cfg.Client, cfg.Namespace, cfg.SecretName, e.changes)
	return e.run(ctx)
}

func newEnroller(cfg EnrollmentConfig) (*enroller, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	origin, _ := NormalizeHubOrigin(cfg.HubOrigin)
	cfg.HubOrigin = origin
	client, err := NewEnrollClient(origin, cfg.KeyFile, cfg.CA)
	if err != nil {
		return nil, err
	}
	return &enroller{
		cfg:       cfg,
		store:     &TokenSecretStore{Client: cfg.Client, Namespace: cfg.Namespace, Name: cfg.SecretName, HubOrigin: origin},
		client:    client,
		changes:   make(chan struct{}, 1),
		runTunnel: Run,
	}, nil
}

func (e *enroller) run(ctx context.Context) error {
	for ctx.Err() == nil {
		st, err := e.store.Load(ctx)
		if err != nil {
			e.logSecretError(err)
			e.waitForChange(ctx, secretRetryDelay, fingerprintMissing)
			continue
		}
		switch {
		case (st.Token != "" && st.HubOrigin != e.store.HubOrigin) || (st.Nonce != "" && st.NonceOrigin != e.store.HubOrigin):
			old := st.HubOrigin
			if st.Token == "" {
				old = st.NonceOrigin
			}
			if old == "" {
				old = "an unknown Hub"
			}
			if _, err := e.store.ClearForeign(ctx, st); err != nil {
				e.logSecretError(err)
				if !errors.Is(err, ErrTokenSecretConflict) {
					e.waitForChange(ctx, secretRetryDelay, fingerprint(st))
				}
				continue
			}
			log.Printf("[cloud] stored credentials belong to %s; joining %s", old, e.store.HubOrigin)
		case st.Status == EnrollStatusRefused && !st.Cleared():
			log.Printf("[cloud] not joining: %s. Waiting for the token Secret %s/%s to be reset", st.Error, e.cfg.Namespace, e.cfg.SecretName)
			e.waitForChange(ctx, 0, fingerprint(st))
		case st.Token != "":
			e.serveTunnel(ctx, st)
		case st.Nonce != "":
			delay := e.attempt(ctx, &st)
			if delay != 0 {
				e.waitForChange(ctx, delay, fingerprint(st))
			}
		default:
			nonce, err := NewEnrollNonce()
			if err != nil {
				return fmt.Errorf("cloud: generate enrollment nonce: %w", err)
			}
			if _, err := e.store.SaveNonce(ctx, st, nonce); err != nil {
				e.logSecretError(err)
				if !errors.Is(err, ErrTokenSecretConflict) {
					e.waitForChange(ctx, secretRetryDelay, fingerprint(st))
				}
				continue
			}
			log.Printf("[cloud] starting to join %s", e.store.HubOrigin)
		}
	}
	return ctx.Err()
}

// attempt makes one enroll call and records the outcome. It returns how long
// to wait before the next decision: 0 to decide again now, a negative value
// to wait until the Secret changes.
func (e *enroller) attempt(ctx context.Context, st *EnrollmentState) time.Duration {
	if e.uid == "" {
		uidCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		uid, err := DiscoverClusterUID(uidCtx, e.cfg.Client)
		cancel()
		if err != nil {
			log.Printf("[cloud] cannot join yet: %v", err)
			*st = e.saveStatus(ctx, *st, EnrollStatusWaiting, err.Error())
			return e.nextBackoff()
		}
		e.uid = uid
		e.name, e.source = ResolveClusterName(e.cfg.Names, e.cfg.Tunnel.APIServerURL, uid)
	}

	req := EnrollRequest{
		AgentUID:     e.uid,
		Namespace:    e.cfg.Namespace,
		Release:      e.cfg.Release,
		Nonce:        st.Nonce,
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
		return 0
	}
	if err == nil {
		if _, err := e.store.SaveJoined(ctx, *st, res); err != nil {
			// The Hub keeps answering this nonce with the same token, so
			// the next attempt picks it up again.
			e.logSecretError(err)
			if errors.Is(err, ErrTokenSecretConflict) {
				return 0
			}
			return secretRetryDelay
		}
		e.backoff = 0
		log.Printf("[cloud] joined %s as cluster %s (%q)", e.store.HubOrigin, res.ClusterID, e.name)
		return 0
	}

	status, msg, delay := classifyEnrollError(err)
	if delay < 0 {
		// Stop only once the refusal is stored: an open-ended wait with
		// nothing recorded would leave the operator no sign of why.
		next, serr := e.store.SaveStatus(ctx, *st, status, msg)
		if serr != nil {
			e.logSecretError(serr)
			if errors.Is(serr, ErrTokenSecretConflict) {
				return 0
			}
			return secretRetryDelay
		}
		*st = next
		log.Printf("[cloud] stopped joining: %v. To join again, %s", err, "run `radar cloud reset-enrollment`")
		return delay
	}
	if delay == 0 {
		delay = e.nextBackoff()
	}
	log.Printf("[cloud] join failed: %v (retry in %s)", err, delay.Round(time.Second))
	*st = e.saveStatus(ctx, *st, status, msg)
	return delay
}

// classifyEnrollError maps an enroll failure to the status Radar records, a
// short message, and a delay: negative stops until the Secret is reset, 0
// means the regular backoff.
func classifyEnrollError(err error) (status, msg string, delay time.Duration) {
	var he *EnrollError
	if !errors.As(err, &he) {
		return EnrollStatusWaiting, err.Error(), 0
	}
	switch {
	case he.StatusCode == http.StatusConflict && he.Code == "enrollment_closed":
		return EnrollStatusRefused, "the Hub closed this join attempt; " + ResetEnrollmentHint, -1
	case he.StatusCode == http.StatusConflict && he.Code == "already_connected":
		return EnrollStatusWaiting, "another Radar with this cluster's identity is connected to the Hub", 0
	case he.StatusCode == http.StatusConflict && he.Code == "no_agent_address":
		return EnrollStatusWaiting, "the Hub has no agent address configured", 0
	case he.StatusCode == http.StatusUnauthorized:
		return EnrollStatusWaiting, "the Hub rejected the enrollment key", 0
	case he.StatusCode == http.StatusPaymentRequired:
		return EnrollStatusWaiting, "the Hub refused to add a cluster: its licence or plan limit is reached", jitter(enrollLongDelay)
	case he.StatusCode == http.StatusServiceUnavailable:
		return EnrollStatusWaiting, "the Hub is not ready to add clusters yet", jitter(enrollNotReadyDelay)
	case he.StatusCode == http.StatusTooManyRequests:
		d := enrollDefaultRateDelay
		if he.HasRetryAfter {
			d = max(he.RetryAfter, time.Second)
		}
		return EnrollStatusWaiting, "the Hub is rate limiting enroll requests", d
	default:
		return EnrollStatusWaiting, he.Error(), 0
	}
}

// serveTunnel runs the tunnel with the stored token until ctx ends, the Hub
// keeps refusing the token, or the Secret's token changes or is cleared.
func (e *enroller) serveTunnel(ctx context.Context, st EnrollmentState) {
	tunnel := e.cfg.Tunnel
	tunnel.URL = st.WSSURL
	tunnel.Token = st.Token
	tunnel.ClusterID = st.ClusterID
	// uidHint also resolves the derived name after a restart, so it runs
	// before the name is read.
	tunnel.ClusterUID = e.uidHint(ctx)
	tunnel.ClusterName = e.displayName()
	tunnel.CA = e.cfg.CA
	tunnel.Enrolled = true

	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.runTunnel(tctx, tunnel) }()

	want := tokenFingerprint(st)
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			<-done
			return
		case err := <-done:
			if errors.Is(err, ErrTokenRejected) {
				msg := "token rejected; " + ResetEnrollmentHint
				log.Printf("[cloud] the Hub keeps rejecting this cluster's token. Not joining again on its own: %s", ResetEnrollmentHint)
				e.recordRefusal(ctx, want, msg)
				return
			}
			if ctx.Err() == nil {
				log.Printf("[cloud] tunnel exited: %v", err)
				st = e.saveStatus(ctx, st, EnrollStatusWaiting, err.Error())
				e.waitForChange(ctx, secretRetryDelay, fingerprint(st))
			}
			return
		case <-retry:
		case <-e.changes:
		}
		retry = nil
		cur, err := e.store.Load(ctx)
		if err != nil && !errors.Is(err, ErrTokenSecretNotFound) {
			retry = time.After(secretRetryDelay)
			continue
		}
		if errors.Is(err, ErrTokenSecretNotFound) || tokenFingerprint(cur) != want {
			log.Printf("[cloud] token Secret %s/%s changed; stopping the tunnel", e.cfg.Namespace, e.cfg.SecretName)
			cancel()
			<-done
			return
		}
	}
}

// recordRefusal stores enroll_status=refused for the token in want. A failed
// read or write is retried: giving up would let the outer loop start the
// tunnel again with the token the Hub just rejected. It stops once the
// refusal is stored, the token changed, or the Secret is gone.
func (e *enroller) recordRefusal(ctx context.Context, want, msg string) {
	for ctx.Err() == nil {
		st, err := e.store.Load(ctx)
		switch {
		case errors.Is(err, ErrTokenSecretNotFound):
			e.logSecretError(err)
			return
		case err != nil:
			e.logSecretError(err)
			sleep(ctx, secretRetryDelay)
			continue
		case tokenFingerprint(st) != want:
			return
		}
		_, err = e.store.SaveStatus(ctx, st, EnrollStatusRefused, msg)
		switch {
		case err == nil:
			return
		case errors.Is(err, ErrTokenSecretConflict):
			continue
		case errors.Is(err, ErrTokenSecretNotFound):
			e.logSecretError(err)
			return
		default:
			e.logSecretError(err)
			sleep(ctx, secretRetryDelay)
		}
	}
}

func (e *enroller) displayName() string {
	if e.name != "" {
		return e.name
	}
	return e.cfg.Names.Exact
}

// uidHint fills the cluster UID for a tunnel started from a stored token,
// where no enroll call has looked it up yet. Failure only drops the hint.
func (e *enroller) uidHint(ctx context.Context) string {
	if e.uid != "" {
		return e.uid
	}
	uidCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if uid, err := DiscoverClusterUID(uidCtx, e.cfg.Client); err == nil {
		e.uid = uid
		e.name, e.source = ResolveClusterName(e.cfg.Names, e.cfg.Tunnel.APIServerURL, uid)
	}
	return e.uid
}

func (e *enroller) saveStatus(ctx context.Context, st EnrollmentState, status, msg string) EnrollmentState {
	next, err := e.store.SaveStatus(ctx, st, status, msg)
	if err != nil {
		if !errors.Is(err, ErrTokenSecretConflict) {
			e.logSecretError(err)
		}
		return st
	}
	return next
}

func (e *enroller) logSecretError(err error) {
	switch {
	case errors.Is(err, ErrTokenSecretConflict):
	case errors.Is(err, ErrTokenSecretNotFound):
		log.Printf("[cloud] token Secret %s/%s not found. Radar never creates it; the Helm chart does. Re-apply the chart to recreate it", e.cfg.Namespace, e.cfg.SecretName)
	default:
		log.Printf("[cloud] %v", err)
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

const fingerprintMissing = "\x00missing"

// fingerprint is the part of the Secret that changes Radar's next decision.
// Status writes Radar makes itself leave it unchanged, so they don't cut a
// backoff short.
func fingerprint(st EnrollmentState) string {
	refused := ""
	if st.Status == EnrollStatusRefused {
		refused = "refused"
	}
	return tokenFingerprint(st) + "\x00" + st.Nonce + "\x00" + st.NonceOrigin + "\x00" + refused
}

func tokenFingerprint(st EnrollmentState) string {
	return st.Token + "\x00" + st.HubOrigin + "\x00" + st.WSSURL + "\x00" + st.ClusterID
}

// waitForChange waits for d (d <= 0: no limit) or until the Secret's
// fingerprint differs from fp.
func (e *enroller) waitForChange(ctx context.Context, d time.Duration, fp string) {
	var timeout <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	// A failed read must not swallow the change it was checking: with no
	// time limit, a reset that arrived during an API hiccup would otherwise
	// go unnoticed until the next unrelated change.
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			return
		case <-e.changes:
		case <-retry:
		}
		retry = nil
		st, err := e.store.Load(ctx)
		switch {
		case errors.Is(err, ErrTokenSecretNotFound):
			if fp != fingerprintMissing {
				return
			}
		case err != nil:
			retry = time.After(secretRetryDelay)
		case fingerprint(st) != fp:
			return
		}
	}
}

// watchTokenSecret signals changes on one Secret. The informer lists and
// watches by name only, which a Role scoped with resourceNames allows.
func watchTokenSecret(ctx context.Context, client kubernetes.Interface, namespace, name string, changes chan<- struct{}) {
	selector := fields.OneTermEqualSelector("metadata.name", name).String()
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			opts.FieldSelector = selector
			return client.CoreV1().Secrets(namespace).List(ctx, opts)
		},
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			opts.FieldSelector = selector
			return client.CoreV1().Secrets(namespace).Watch(ctx, opts)
		},
	}
	informer := cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(lw, client), &corev1.Secret{}, 0, cache.Indexers{})
	notify := func(obj any) {
		if tomb, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = tomb.Obj
		}
		if s, ok := obj.(*corev1.Secret); ok && s.Name != name {
			return
		}
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    notify,
		UpdateFunc: func(_, obj any) { notify(obj) },
		DeleteFunc: notify,
	})
	go informer.RunWithContext(ctx)
}
