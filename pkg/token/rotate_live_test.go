//go:build live

// Live verification of admin token rotation against a real Vault dev
// server and a real kube-apiserver (envtest). Opt-in:
//
//	KUBEBUILDER_ASSETS=$(bin/setup-envtest use 1.29.0 --bin-dir bin -p path) \
//	  go test -tags live ./pkg/token -run Live -v -count=1
//
// A fault-injecting transport sits between the operator's client and the
// apiserver, so a Secret write can be failed, held, committed with its
// response lost, or released only after a later read.
package token

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// writeKind classifies a PUT of the admin Secret by what it changes.
type writeKind string

const (
	kindSwap    writeKind = "swap"
	kindLedger  writeKind = "ledger"
	kindFence   writeKind = "fence"
	kindBackoff writeKind = "backoff"
	kindOther   writeKind = "other"
)

// action is what the fault transport does with one Secret write.
type action int

const (
	actPass         action = iota
	actFail500             // answer 500, never forward: uncertain, never lands
	actCommitLost          // forward, then answer a timeout: uncertain, landed
	actHoldForward         // hold 5s past the 2s deadline, then forward detached
	actHoldUntilGet        // hold until the next GET of the Secret has returned, then forward
)

type faultTransport struct {
	next   http.RoundTripper
	direct client.Client // unwrapped, to classify writes against stored state

	mu       sync.Mutex
	rule     func(kind writeKind) action
	writes   []writeKind
	waitGet  chan struct{} // closed when the holdGets-th Secret GET after a hold returns
	holdGets int
	gets     int
}

func (f *faultTransport) setRule(r func(writeKind) action) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rule, f.writes = r, nil
}

func isAdminSecret(req *http.Request) bool {
	return strings.HasSuffix(req.URL.Path, "/namespaces/vault/secrets/vault-admin-token")
}

func (f *faultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isAdminSecret(req) {
		return f.next.RoundTrip(req)
	}
	if req.Method == http.MethodGet {
		resp, err := f.next.RoundTrip(req)
		if err == nil {
			// Buffer the body so the read has fully happened before release.
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(b))
		}
		f.mu.Lock()
		if f.waitGet != nil {
			f.gets++
			if f.gets >= f.holdGets {
				close(f.waitGet)
				f.waitGet = nil
			}
		}
		f.mu.Unlock()
		return resp, err
	}
	if req.Method != http.MethodPut {
		return f.next.RoundTrip(req)
	}

	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	kind := f.classify(req.Context(), body)
	f.mu.Lock()
	f.writes = append(f.writes, kind)
	act := actPass
	if f.rule != nil {
		act = f.rule(kind)
	}
	f.mu.Unlock()

	detached := req.Clone(context.Background())
	detached.Body = io.NopCloser(bytes.NewReader(body))
	switch act {
	case actFail500:
		return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}},
			Body:    io.NopCloser(strings.NewReader(`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"injected","reason":"InternalError","code":500}`)),
			Request: req}, nil
	case actCommitLost:
		resp, err := f.next.RoundTrip(detached)
		if err == nil {
			resp.Body.Close()
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	case actHoldForward:
		go func() {
			time.Sleep(5 * time.Second)
			if resp, err := f.next.RoundTrip(detached); err == nil {
				resp.Body.Close()
			}
		}()
		<-req.Context().Done()
		return nil, req.Context().Err()
	case actHoldUntilGet:
		ch := make(chan struct{})
		f.mu.Lock()
		f.waitGet, f.gets = ch, 0
		if f.holdGets == 0 {
			f.holdGets = 1
		}
		f.mu.Unlock()
		go func() {
			<-ch
			if resp, err := f.next.RoundTrip(detached); err == nil {
				resp.Body.Close()
			}
		}()
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return f.next.RoundTrip(req)
}

func (f *faultTransport) classify(ctx context.Context, body []byte) writeKind {
	var written corev1.Secret
	if json.Unmarshal(body, &written) != nil {
		return kindOther
	}
	stored := &corev1.Secret{}
	if f.direct.Get(ctx, client.ObjectKey{Namespace: "vault", Name: "vault-admin-token"}, stored) != nil {
		return kindOther
	}
	switch {
	case !bytes.Equal(written.Data["token"], stored.Data["token"]):
		return kindSwap
	case written.Annotations[fenceAnnotation] != stored.Annotations[fenceAnnotation]:
		return kindFence
	case written.Annotations[backoffAnnotation] != stored.Annotations[backoffAnnotation]:
		return kindBackoff
	}
	w, _ := readLedger(&written)
	s, _ := readLedger(stored)
	for _, e := range w {
		if e.State == stateUnresolved && !hasUnresolvedMint(s, e.SwapID) {
			return kindLedger
		}
	}
	return kindOther
}

// countingVault counts token creations made through it.
type countingVault struct {
	next    http.RoundTripper
	creates atomic.Int64
}

func (c *countingVault) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/auth/token/create") {
		c.creates.Add(1)
	}
	return c.next.RoundTrip(req)
}

type live struct {
	t       *testing.T
	root    *vaultapi.Client
	op      *vaultapi.Client
	counter *countingVault
	fault   *faultTransport
	direct  client.Client
	m       *SimpleManager
	vtu     *vaultv1alpha1.VaultTransitUnseal
	offset  time.Duration
	events  *record.FakeRecorder
	origAcc string
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

var (
	envOnce sync.Once
	envCfg  *rest.Config
	envErr  error
)

func sharedAPIServer(t *testing.T) *rest.Config {
	envOnce.Do(func() {
		env := &envtest.Environment{}
		envCfg, envErr = env.Start()
	})
	require.NoError(t, envErr)
	return envCfg
}

func newLive(t *testing.T) *live {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cmd := exec.Command("vault", "server", "-dev", "-dev-root-token-id=root", "-dev-listen-address="+addr)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	cfg := vaultapi.DefaultConfig()
	cfg.Address = "http://" + addr
	cfg.MaxRetries = 0
	root, err := vaultapi.NewClient(cfg)
	require.NoError(t, err)
	root.SetToken("root")
	require.Eventually(t, func() bool { _, err := root.Sys().Health(); return err == nil }, 10*time.Second, 100*time.Millisecond)
	require.NoError(t, root.Sys().PutPolicy("vault-admin",
		`path "*" { capabilities = ["create","read","update","delete","list","sudo"] }`))

	renewable := true
	admin, err := root.Auth().Token().Create(&vaultapi.TokenCreateRequest{
		Policies: []string{"vault-admin"}, Period: "168h", NoParent: true, Renewable: &renewable})
	require.NoError(t, err)

	counter := &countingVault{next: http.DefaultTransport}
	opCfg := vaultapi.DefaultConfig()
	opCfg.Address = cfg.Address
	opCfg.MaxRetries = 0
	opCfg.HttpClient = &http.Client{Transport: counter, Timeout: 10 * time.Second}
	op, err := vaultapi.NewClient(opCfg)
	require.NoError(t, err)

	restCfg := rest.CopyConfig(sharedAPIServer(t))
	direct, err := client.New(restCfg, client.Options{Scheme: newScheme(t)})
	require.NoError(t, err)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "vault"}}
	_ = direct.Create(context.Background(), ns)
	_ = direct.Delete(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "vault-admin-token", Namespace: "vault"}})
	require.Eventually(t, func() bool {
		return direct.Create(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "vault-admin-token", Namespace: "vault"},
			Data:       map[string][]byte{"token": []byte(admin.Auth.ClientToken)},
		}) == nil
	}, 10*time.Second, 100*time.Millisecond)

	fault := &faultTransport{direct: direct}
	wrapped := rest.CopyConfig(restCfg)
	// JSON, so the fault transport can read what each write changes.
	wrapped.ContentType = "application/json"
	wrapped.AcceptContentTypes = "application/json"
	wrapped.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		fault.next = rt
		return fault
	}
	c, err := client.New(wrapped, client.Options{Scheme: newScheme(t)})
	require.NoError(t, err)

	l := &live{t: t, root: root, op: op, counter: counter, fault: fault, direct: direct,
		events: record.NewFakeRecorder(1000), origAcc: admin.Auth.Accessor}
	l.vtu = &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{Name: "vault-homelab", Namespace: "vault"},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			VaultPod:       vaultv1alpha1.VaultPodSpec{Namespace: "vault"},
			Initialization: vaultv1alpha1.InitializationSpec{SecretNames: vaultv1alpha1.SecretNamesSpec{AdminToken: "vault-admin-token"}},
			TokenManagement: &vaultv1alpha1.TokenManagementSpec{
				Enabled: true, Strategy: vaultv1alpha1.TokenStrategyImmediate, PolicyName: "vault-admin",
				TTL: "168h", AutoRenew: true, AutoRotate: false, RotationPeriod: "720h", RotationGracePeriod: "1m",
			},
		},
	}
	l.m = l.manager(c)
	return l
}

// manager is a fresh operator process sharing the faulty client.
func (l *live) manager(c client.Client) *SimpleManager {
	m := &SimpleManager{Client: c, APIReader: c, Log: testr.New(l.t), Events: l.events}
	m.clock = func() time.Time { return time.Now().Add(l.offset) }
	return m
}

func (l *live) crash() { l.m = l.manager(l.m.Client) }

func (l *live) pass() error {
	if err := l.m.RenewIfNeeded(context.Background(), l.vtu, l.op); err != nil {
		l.t.Errorf("renewal error: %v", err)
	}
	return l.m.RotateIfDue(context.Background(), l.vtu, l.op)
}

// rotateOnly runs RotateIfDue without the renew read before it, so the
// sequence of Secret reads is exactly the rotation's.
func (l *live) rotateOnly() error {
	return l.m.RotateIfDue(context.Background(), l.vtu, l.op)
}

func (l *live) deannotate(k string) {
	require.Eventually(l.t, func() bool {
		s := l.secret()
		delete(s.Annotations, k)
		return l.direct.Update(context.Background(), s) == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func (l *live) secret() *corev1.Secret {
	s := &corev1.Secret{}
	require.NoError(l.t, l.direct.Get(context.Background(), client.ObjectKey{Namespace: "vault", Name: "vault-admin-token"}, s))
	return s
}

func (l *live) annotate(k, v string) {
	require.Eventually(l.t, func() bool {
		s := l.secret()
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[k] = v
		return l.direct.Update(context.Background(), s) == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func (l *live) accessorValid(acc string) bool {
	_, err := l.root.Auth().Token().LookupAccessor(acc)
	return err == nil
}

func (l *live) currentAccessor() string {
	info, err := l.root.Auth().Token().Lookup(string(l.secret().Data["token"]))
	require.NoError(l.t, err)
	return info.Data["accessor"].(string)
}

// tokens created by the operator (metadata source=rotation) and still valid.
func (l *live) validMinted() []string {
	var out []string
	secret, err := l.root.Logical().List("auth/token/accessors")
	require.NoError(l.t, err)
	for _, a := range secret.Data["keys"].([]any) {
		info, err := l.root.Auth().Token().LookupAccessor(a.(string))
		if err != nil {
			continue
		}
		if meta, ok := info.Data["meta"].(map[string]any); ok && meta["source"] == "rotation" {
			out = append(out, a.(string))
		}
	}
	return out
}

func (l *live) ledger() []LedgerEntry {
	e, err := readLedger(l.secret())
	require.NoError(l.t, err)
	return e
}

// invariant: the Secret holds a valid token, every valid rotation token
// is the live one, and the ledger holds no unresolved entry.
func (l *live) requireSettled() {
	cur := l.currentAccessor()
	require.True(l.t, l.accessorValid(cur), "the Secret's token is valid")
	for _, acc := range l.validMinted() {
		require.Equal(l.t, cur, acc, "a minted token is either installed or revoked")
	}
	for _, e := range l.ledger() {
		require.Equal(l.t, stateScheduled, e.State)
	}
}

// requireInjected fails unless the fault transport saw a write of kind.
func (l *live) requireInjected(kind writeKind) {
	l.fault.mu.Lock()
	defer l.fault.mu.Unlock()
	for _, k := range l.fault.writes {
		if k == kind {
			return
		}
	}
	l.t.Fatalf("no %s write reached the fault transport (saw %v)", kind, l.fault.writes)
}

func (l *live) record(format string, args ...any) {
	l.t.Logf("OBSERVED: "+format, args...)
}

// Row: token older than rotationPeriod, grace 1m, 3 pod passes.
func TestLiveScheduledRotation(t *testing.T) {
	l := newLive(t)
	l.vtu.Spec.TokenManagement.AutoRotate = true
	l.vtu.Spec.TokenManagement.RotationPeriod = "2s"
	time.Sleep(3 * time.Second)
	for i := 0; i < 3; i++ {
		require.NoError(t, l.pass())
	}
	require.EqualValues(t, 1, l.counter.creates.Load())
	require.NotEqual(t, l.origAcc, l.currentAccessor())
	require.True(t, l.accessorValid(l.origAcc), "old token valid during grace")

	l.vtu.Spec.TokenManagement.AutoRotate = false // isolate the revoke from further rotations
	l.offset = 61 * time.Second
	require.NoError(t, l.pass())
	require.False(t, l.accessorValid(l.origAcc))
	require.Empty(t, l.ledger())
	l.record("creates=%d after 3 passes; old accessor valid during grace, invalid after +61s; ledger empty; 0 renewal errors",
		l.counter.creates.Load())
}

// Row: autoRotate=false + rotate-now rotates; enabled=false + rotate-now does not.
func TestLiveForcedAndNotOwned(t *testing.T) {
	l := newLive(t)
	l.annotate(rotateNowAnnotation, "true")
	require.NoError(t, l.pass())
	require.EqualValues(t, 1, l.counter.creates.Load())
	require.NotContains(t, l.secret().Annotations, rotateNowAnnotation)

	l.vtu.Spec.TokenManagement.Enabled = false
	l.annotate(rotateNowAnnotation, "true")
	require.NoError(t, l.pass())
	require.EqualValues(t, 1, l.counter.creates.Load())
	var skipped bool
	for len(l.events.Events) > 0 {
		if strings.Contains(<-l.events.Events, "TokenRotationSkipped") {
			skipped = true
		}
	}
	require.True(t, skipped)
	l.record("autoRotate=false+rotate-now: 1 create, annotation cleared; enabled=false+rotate-now: still 1 create, TokenRotationSkipped")
}

// Row: the swap PUT is held 5s, past the 2s deadline, then forwarded.
func TestLiveHeldSwap(t *testing.T) {
	l := newLive(t)
	l.annotate(rotateNowAnnotation, "true")
	l.fault.setRule(func(k writeKind) action {
		if k == kindSwap {
			return actHoldForward
		}
		return actPass
	})
	require.Error(t, l.pass())
	l.fault.setRule(nil)
	time.Sleep(6 * time.Second) // the held write reaches the apiserver
	l.deannotate(rotateNowAnnotation)
	held := l.validMinted()
	l.record("after the held swap was forwarded: token is minted=%v; valid minted=%d",
		len(held) == 1 && held[0] == l.currentAccessor(), len(held))
	l.crash()
	require.NoError(t, l.pass())
	l.offset = 2 * time.Minute
	require.NoError(t, l.pass())
	l.requireSettled()
	l.record("settled: current=%s; valid minted=%v; ledger=%+v", l.currentAccessor(), l.validMinted(), l.ledger())
}

// Row (the person's case): the swap commits only after the fence's
// verification read, with the backoff write lost so nothing else moves
// the Secret in between.
func TestLiveSwapCommitsAfterVerificationRead(t *testing.T) {
	l := newLive(t)
	l.annotate(rotateNowAnnotation, "true")
	// Secret reads after the swap is held: 1 the backoff write's read in
	// this pass, 2 the next pass's own read, 3 the fence's verification
	// read. The swap is released only after read 3 returns.
	l.fault.holdGets = 3
	l.fault.setRule(func(k writeKind) action {
		switch k {
		case kindSwap:
			return actHoldUntilGet
		case kindBackoff:
			return actFail500
		}
		return actPass
	})
	require.Error(t, l.rotateOnly())
	l.fault.mu.Lock()
	l.fault.rule = nil
	l.fault.mu.Unlock()
	minted := l.validMinted()
	require.Len(t, minted, 1)
	l.crash()
	require.NoError(t, l.rotateOnly())
	l.fault.mu.Lock()
	writes := append([]writeKind(nil), l.fault.writes...)
	l.fault.mu.Unlock()
	l.record("writes seen in the settling pass: %v", writes)
	require.NoError(t, l.rotateOnly())
	require.Equal(t, minted[0], l.currentAccessor(), "the swap landed and the minted token is live")
	require.True(t, l.accessorValid(minted[0]))
	l.requireSettled()
	l.record("late-committing swap: minted %s is live and valid; ledger=%+v", minted[0], l.ledger())
}

// Row: crash, and separately an uncertain result, after each of ledger
// write, swap and fence.
func TestLiveFaultMatrix(t *testing.T) {
	cases := []struct {
		name  string
		kind  writeKind
		act   action
		crash bool
	}{
		{"ledger lost after commit", kindLedger, actCommitLost, false},
		{"ledger lost after commit, crash", kindLedger, actCommitLost, true},
		{"ledger 500", kindLedger, actFail500, true},
		{"swap lost after commit", kindSwap, actCommitLost, false},
		{"swap lost after commit, crash", kindSwap, actCommitLost, true},
		{"swap 500", kindSwap, actFail500, false},
		{"swap 500, crash", kindSwap, actFail500, true},
		{"fence lost after commit", kindFence, actCommitLost, false},
		{"fence lost after commit, crash", kindFence, actCommitLost, true},
		{"fence 500, crash", kindFence, actFail500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLive(t)
			l.annotate(rotateNowAnnotation, "true")
			// A fence only runs when a swap is uncertain: make it so.
			swapFirst := tc.kind == kindFence
			once := false
			l.fault.setRule(func(k writeKind) action {
				if swapFirst && k == kindSwap {
					return actFail500
				}
				if k == tc.kind && !once {
					once = true
					return tc.act
				}
				return actPass
			})
			_ = l.pass()
			if tc.kind != kindFence {
				l.requireInjected(tc.kind)
			}
			if tc.crash {
				l.crash()
			}
			for i := 0; i < 3; i++ {
				_ = l.pass()
			}
			l.requireInjected(tc.kind)
			mintedDuringFault := l.counter.creates.Load()
			l.fault.setRule(nil)
			l.deannotate(rotateNowAnnotation)
			l.offset = 2 * time.Minute
			require.NoError(t, l.pass())
			l.requireSettled()
			require.Empty(t, l.ledger())
			l.record("%s: mints during fault=%d, swapped=%v, valid rotation tokens=%d, ledger=%d",
				tc.name, mintedDuringFault, l.currentAccessor() != l.origAcc, len(l.validMinted()), len(l.ledger()))
		})
	}
}

// Row: an hour of swap failures (500 only on the token-changing PUT).
func TestLiveHourOfSwapFailures(t *testing.T) {
	l := newLive(t)
	l.annotate(rotateNowAnnotation, "true")
	l.fault.setRule(func(k writeKind) action {
		if k == kindSwap {
			return actFail500
		}
		return actPass
	})
	var mintsAt []time.Duration
	for elapsed := time.Duration(0); elapsed <= time.Hour; elapsed += 30 * time.Second {
		l.offset = elapsed
		before := l.counter.creates.Load()
		_ = l.pass()
		if l.counter.creates.Load() > before {
			mintsAt = append(mintsAt, elapsed)
		}
		if n := len(l.ledger()); n > ledgerCap {
			t.Fatalf("ledger grew to %d", n)
		}
	}
	l.fault.setRule(nil)
	_ = l.pass() // revoke the last minted token
	require.Equal(t, []time.Duration{0, time.Minute, 3 * time.Minute, 7 * time.Minute, 15 * time.Minute, 31 * time.Minute}, mintsAt)
	require.Empty(t, l.validMinted())
	require.Empty(t, l.ledger())
	require.Equal(t, l.origAcc, l.currentAccessor())
	l.record("mints at %v; valid minted after: %d; ledger: %d", mintsAt, len(l.validMinted()), len(l.ledger()))
}

var _ = errors.New
