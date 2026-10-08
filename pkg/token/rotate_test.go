package token

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/metrics"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

const currentToken = "hvs.current-token"

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// harness drives RotateIfDue against a fake Vault and a fake apiserver
// whose writes can be failed, timed out, or committed late.
type harness struct {
	t      *testing.T
	fv     *fakeVault
	base   client.WithWatch
	c      client.WithWatch
	m      *SimpleManager
	vtu    *vaultv1alpha1.VaultTransitUnseal
	events *record.FakeRecorder
	now    time.Time

	currentAccessor string

	// onUpdate, when set, decides the n-th Update (1-based) of the armed
	// window: return nil to let it through, or an error to fail it
	// without applying. delay=true stores it for a late commit.
	onUpdate func(n int, s *corev1.Secret) (err error, delay bool)
	// onGet, when set, may fail the n-th Get of the armed window.
	onGet   func(n int) error
	updates int
	gets    int
	delayed *corev1.Secret
	// commitAfterGet commits the delayed write right after that Get.
	commitAfterGet int
}

func newHarness(t *testing.T, tokenAge time.Duration) *harness {
	t.Helper()
	h := &harness{t: t, fv: newFakeVault(t), now: t0}
	h.currentAccessor = h.fv.add(currentToken, t0.Add(-tokenAge))

	h.vtu = &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{Name: "vault-homelab", Namespace: "vault"},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			VaultPod: vaultv1alpha1.VaultPodSpec{
				Namespace: "vault",
				Selector:  map[string]string{"app.kubernetes.io/name": "vault"},
			},
			Initialization: vaultv1alpha1.InitializationSpec{
				SecretNames: vaultv1alpha1.SecretNamesSpec{AdminToken: "vault-admin-token"},
			},
			TokenManagement: &vaultv1alpha1.TokenManagementSpec{
				Enabled:             true,
				Strategy:            vaultv1alpha1.TokenStrategyImmediate,
				PolicyName:          "vault-admin",
				TTL:                 "168h",
				AutoRenew:           true,
				AutoRotate:          true,
				RotationPeriod:      "720h",
				RotationGracePeriod: "1h",
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vault-admin-token", Namespace: "vault"},
		Data:       map[string][]byte{"token": []byte(currentToken)},
	}
	h.base = fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(secret).Build()
	h.c = interceptor.NewClient(h.base, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			s, ok := obj.(*corev1.Secret)
			if !ok || h.onUpdate == nil {
				return c.Update(ctx, obj, opts...)
			}
			h.updates++
			err, delay := h.onUpdate(h.updates, s)
			if err == nil {
				return c.Update(ctx, obj, opts...)
			}
			if delay {
				h.delayed = s.DeepCopy()
			}
			return err
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				h.gets++
				if h.onGet != nil {
					if err := h.onGet(h.gets); err != nil {
						return err
					}
				}
				err := c.Get(ctx, key, obj, opts...)
				if h.delayed != nil && h.commitAfterGet == h.gets {
					require.NoError(h.t, c.Update(ctx, h.delayed), "late commit")
					h.delayed = nil
				}
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	h.events = record.NewFakeRecorder(100)
	h.m = h.newManager()
	return h
}

// newManager is a fresh operator process: no in-memory backoff.
func (h *harness) newManager() *SimpleManager {
	m := &SimpleManager{
		Client:    h.c,
		APIReader: h.c,
		Log:       testr.New(h.t),
		Events:    h.events,
	}
	m.clock = func() time.Time { return h.now }
	return m
}

// arm resets the hook counters so n counts from the next call.
func (h *harness) arm() {
	h.updates, h.gets = 0, 0
}

func (h *harness) pass() error {
	h.t.Helper()
	return h.m.RotateIfDue(context.Background(), h.vtu, h.fv.client())
}

func (h *harness) secret() *corev1.Secret {
	h.t.Helper()
	s := &corev1.Secret{}
	require.NoError(h.t, h.base.Get(context.Background(),
		client.ObjectKey{Namespace: "vault", Name: "vault-admin-token"}, s))
	return s
}

func (h *harness) token() string { return string(h.secret().Data["token"]) }

func (h *harness) ledger() []LedgerEntry {
	h.t.Helper()
	l, err := readLedger(h.secret())
	require.NoError(h.t, err)
	return l
}

func (h *harness) annotate(key, value string) {
	h.t.Helper()
	s := h.secret()
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[key] = value
	require.NoError(h.t, h.base.Update(context.Background(), s))
}

func (h *harness) seedLedger(entries ...LedgerEntry) {
	h.t.Helper()
	s := h.secret()
	writeLedger(s, entries)
	require.NoError(h.t, h.base.Update(context.Background(), s))
}

// setToken replaces the Secret's token as another writer would, keeping
// every annotation.
func (h *harness) setToken(value string) {
	h.t.Helper()
	s := h.secret()
	s.Data["token"] = []byte(value)
	require.NoError(h.t, h.base.Update(context.Background(), s))
}

func (h *harness) drainEvents() []string {
	var out []string
	for {
		select {
		case e := <-h.events.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func hasEvent(events []string, reason string) bool {
	for _, e := range events {
		if strings.Contains(e, " "+reason+" ") {
			return true
		}
	}
	return false
}

func mintedAccessors(fv *fakeVault) []string {
	var out []string
	fv.mu.Lock()
	defer fv.mu.Unlock()
	for acc := range fv.accessor {
		if strings.Contains(acc, "minted") {
			out = append(out, acc)
		}
	}
	return out
}

var conflict = apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "vault-admin-token", errors.New("object was modified"))
var timeout = apierrors.NewTimeoutError("request did not complete within 2s", 1)

// isSwap reports whether an Update changes the Secret's token.
func (h *harness) isSwap(s *corev1.Secret) bool {
	return string(s.Data["token"]) != h.token()
}

// isLedgerWrite reports whether an Update records a new mint before its
// swap: same token, an unresolved mint the stored Secret does not have.
func (h *harness) isLedgerWrite(s *corev1.Secret) bool {
	if h.isSwap(s) {
		return false
	}
	written, _ := readLedger(s)
	stored := h.ledger()
	for _, e := range written {
		if e.State == stateUnresolved && !hasUnresolvedMint(stored, e.SwapID) {
			return true
		}
	}
	return false
}

// isBackoffWrite reports whether an Update only records the backoff.
func (h *harness) isBackoffWrite(s *corev1.Secret) bool {
	return !h.isSwap(s) && s.Annotations[backoffAnnotation] != h.secret().Annotations[backoffAnnotation]
}

func TestRotateNotDue(t *testing.T) {
	h := newHarness(t, time.Hour)
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/create"))
	require.Empty(t, h.ledger())
	require.Equal(t, currentToken, h.token())
}

func TestRotateDueByAgeThenRevokeAfterGrace(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	require.NoError(t, h.pass())

	require.Len(t, h.fv.callsOf("auth/token/create"), 1)
	minted := h.token()
	require.NotEqual(t, currentToken, minted)
	require.Equal(t, "acc-"+minted, h.secret().Annotations["vault.homelab.io/token-accessor"])
	require.Equal(t, []LedgerEntry{{
		Accessor: h.currentAccessor, NotBefore: t0.Add(time.Hour),
		Reason: reasonSuperseded, State: stateScheduled, SwapID: h.ledger()[0].SwapID,
	}}, h.ledger())
	require.True(t, hasEvent(h.drainEvents(), "TokenRotated"))

	// Two more pod passes in the same reconcile mint nothing: the new
	// token is age zero.
	require.NoError(t, h.pass())
	require.NoError(t, h.pass())
	require.Len(t, h.fv.callsOf("auth/token/create"), 1)

	h.now = t0.Add(30 * time.Minute)
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/revoke-accessor"))
	require.True(t, h.fv.valid(h.currentAccessor))

	h.now = t0.Add(61 * time.Minute)
	require.NoError(t, h.pass())
	require.Equal(t, []string{"auth/token/revoke-accessor " + h.currentAccessor}, h.fv.callsOf("auth/token/revoke-accessor"))
	require.Equal(t, []string{"auth/token/lookup-accessor " + h.currentAccessor}, h.fv.callsOf("auth/token/lookup-accessor"))
	require.False(t, h.fv.valid(h.currentAccessor))
	require.Empty(t, h.ledger())
	require.True(t, h.fv.valid("acc-"+minted))
}

func TestRotateNowOverridesAutoRotateOff(t *testing.T) {
	h := newHarness(t, time.Hour)
	h.vtu.Spec.TokenManagement.AutoRotate = false
	h.annotate(rotateNowAnnotation, "true")
	require.NoError(t, h.pass())
	require.Len(t, h.fv.callsOf("auth/token/create"), 1)
	require.NotContains(t, h.secret().Annotations, rotateNowAnnotation)
	require.NotContains(t, h.secret().Annotations, "vault.homelab.io/auto-rotate")
}

func TestRotateNowWhilePendingKeepsBothEntries(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	require.NoError(t, h.pass())
	first := h.token()
	h.annotate(rotateNowAnnotation, "true")
	require.NoError(t, h.pass())

	require.Len(t, h.fv.callsOf("auth/token/create"), 2)
	l := h.ledger()
	require.Len(t, l, 2)
	require.Equal(t, h.currentAccessor, l[0].Accessor)
	require.Equal(t, "acc-"+first, l[1].Accessor)
}

func TestRotateNowImmediateRevokesNextPass(t *testing.T) {
	h := newHarness(t, time.Hour)
	h.annotate(rotateNowAnnotation, "immediate")
	require.NoError(t, h.pass())
	l := h.ledger()
	require.Len(t, l, 1)
	require.Equal(t, reasonForced, l[0].Reason)
	require.Equal(t, t0, l[0].NotBefore)

	require.NoError(t, h.pass())
	require.False(t, h.fv.valid(h.currentAccessor))
	require.Empty(t, h.ledger())
}

func TestCancelRevokeLeavesTokenValid(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	require.NoError(t, h.pass())
	h.annotate(cancelRevokeAnnotation, h.currentAccessor)
	h.now = t0.Add(2 * time.Hour)
	require.NoError(t, h.pass())

	require.Empty(t, h.fv.callsOf("auth/token/revoke-accessor"))
	require.True(t, h.fv.valid(h.currentAccessor))
	require.Empty(t, h.ledger())
	require.NotContains(t, h.secret().Annotations, cancelRevokeAnnotation)
	require.True(t, hasEvent(h.drainEvents(), "TokenRevokeCancelled"))
}

func TestNotOwnedNeverMintsButLedgerDrains(t *testing.T) {
	for name, configure := range map[string]func(*vaultv1alpha1.TokenManagementSpec){
		"enabled=false":     func(tm *vaultv1alpha1.TokenManagementSpec) { tm.Enabled = false },
		"strategy=external": func(tm *vaultv1alpha1.TokenManagementSpec) { tm.Strategy = vaultv1alpha1.TokenStrategyExternal },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 800*time.Hour)
			retired := h.fv.add("hvs.retired-token", t0.Add(-900*time.Hour))
			h.seedLedger(LedgerEntry{Accessor: retired, NotBefore: t0.Add(-time.Minute),
				Reason: reasonSuperseded, State: stateScheduled, SwapID: "s0"})
			configure(h.vtu.Spec.TokenManagement)
			h.annotate(rotateNowAnnotation, "true")

			require.NoError(t, h.pass())
			require.Empty(t, h.fv.callsOf("auth/token/create"))
			require.True(t, hasEvent(h.drainEvents(), "TokenRotationSkipped"))
			require.False(t, h.fv.valid(retired))
			require.Empty(t, h.ledger())
			require.Equal(t, currentToken, h.token())
		})
	}
}

func TestAutoRotateOffStillRevokesPending(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	require.NoError(t, h.pass())
	h.vtu.Spec.TokenManagement.AutoRotate = false
	h.now = t0.Add(2 * time.Hour)
	require.NoError(t, h.pass())
	require.False(t, h.fv.valid(h.currentAccessor))
	require.Empty(t, h.ledger())
}

func TestInvalidCurrentTokenMintsNothing(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.fv.invalidate(h.currentAccessor)
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/create"))
	require.Equal(t, currentToken, h.token())
}

func TestLookupTimeoutDoesNothing(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	retired := h.fv.add("hvs.retired-token", t0.Add(-900*time.Hour))
	h.seedLedger(LedgerEntry{Accessor: retired, NotBefore: t0.Add(-time.Minute),
		Reason: reasonSuperseded, State: stateScheduled, SwapID: "s0"})
	// A successful pass at t0-1h sets the observation; the failing one
	// at t0 must not move it.
	h.now = t0.Add(-time.Hour)
	h.fv.mu.Lock()
	h.fv.revokeIsNoop = true
	h.fv.mu.Unlock()
	h.vtu.Spec.TokenManagement.AutoRotate = false
	require.NoError(t, h.pass())
	observed := gauge(t, "vault_admin_token_last_observation_timestamp_seconds")
	require.Equal(t, float64(t0.Add(-time.Hour).Unix()), observed)

	h.fv.resetCalls()
	h.fv.mu.Lock()
	h.fv.lookupSelfFails = true
	h.fv.mu.Unlock()
	h.vtu.Spec.TokenManagement.AutoRotate = true
	h.now = t0
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/revoke-accessor"))
	require.Empty(t, h.fv.callsOf("auth/token/create"))
	require.Equal(t, observed, gauge(t, "vault_admin_token_last_observation_timestamp_seconds"))
}

func TestNeverRevokesTheCurrentAccessor(t *testing.T) {
	h := newHarness(t, time.Hour)
	h.seedLedger(LedgerEntry{Accessor: h.currentAccessor, NotBefore: t0.Add(-time.Minute),
		Reason: reasonSuperseded, State: stateScheduled, SwapID: "s0"})
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/revoke-accessor"))
	require.True(t, h.fv.valid(h.currentAccessor))
	require.Empty(t, h.ledger())
	require.True(t, hasEvent(h.drainEvents(), "TokenRevokeSkippedCurrent"))
}

func TestUnverifiedRevokeKeepsTheEntry(t *testing.T) {
	h := newHarness(t, time.Hour)
	retired := h.fv.add("hvs.retired-token", t0.Add(-900*time.Hour))
	h.seedLedger(LedgerEntry{Accessor: retired, NotBefore: t0.Add(-time.Minute),
		Reason: reasonSuperseded, State: stateScheduled, SwapID: "s0"})
	h.fv.revokeIsNoop = true
	require.NoError(t, h.pass())
	require.Len(t, h.fv.callsOf("auth/token/lookup-accessor"), 1)
	require.Len(t, h.ledger(), 1)
	require.True(t, hasEvent(h.drainEvents(), "TokenRotationFailed"))
}

func TestOutsideWriterWhilePendingRevokesOnlyLedgered(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	require.NoError(t, h.pass())
	rotated := h.token()
	h.fv.add("hvs.outside-token", t0)
	h.setToken("hvs.outside-token")

	h.now = t0.Add(2 * time.Hour)
	require.NoError(t, h.pass())
	require.Equal(t, []string{"auth/token/revoke-accessor " + h.currentAccessor}, h.fv.callsOf("auth/token/revoke-accessor"))
	require.True(t, h.fv.valid("acc-"+rotated))
	require.True(t, h.fv.valid("acc-hvs.outside-token"))
}

func TestLedgerWriteRejectedRevokesMintAtOnce(t *testing.T) {
	for name, failure := range map[string]error{"definite": conflict, "uncertain": timeout} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 800*time.Hour)
			h.arm()
			h.onUpdate = func(_ int, s *corev1.Secret) (error, bool) {
				if h.isLedgerWrite(s) {
					return failure, false
				}
				return nil, false
			}
			require.Error(t, h.pass())
			require.Len(t, h.fv.callsOf("auth/token/create"), 1)
			require.Empty(t, mintedAccessors(h.fv), "the minted token is revoked")
			require.Equal(t, currentToken, h.token())
			require.Empty(t, h.ledger())
		})
	}
}

func TestUncertainLedgerWriteThatLandsLaterDrains(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.arm()
	h.onUpdate = func(_ int, s *corev1.Secret) (error, bool) {
		if h.isLedgerWrite(s) {
			return timeout, true
		}
		if h.isBackoffWrite(s) {
			return timeout, false // lost, so the ledger write can still land
		}
		return nil, false
	}
	require.Error(t, h.pass())
	require.Empty(t, mintedAccessors(h.fv))
	// The timed-out ledger write commits after all.
	require.NoError(t, h.base.Update(context.Background(), h.delayed))
	h.delayed = nil
	require.Len(t, h.ledger(), 2)

	h.onUpdate = nil
	require.NoError(t, h.pass())
	require.Empty(t, h.ledger())
	require.Equal(t, currentToken, h.token())
	require.True(t, h.fv.valid(h.currentAccessor))
}

func TestSwapConflictSchedulesMintedRevocation(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.arm()
	h.onUpdate = func(n int, s *corev1.Secret) (error, bool) {
		if h.isSwap(s) {
			return conflict, false
		}
		return nil, false
	}
	require.Error(t, h.pass())
	require.Equal(t, currentToken, h.token())
	l := h.ledger()
	require.Len(t, l, 1, "the old entry is dropped: it is the live token")
	require.Equal(t, reasonUnresolvedMint, l[0].Reason)
	require.Equal(t, stateScheduled, l[0].State)

	h.onUpdate = nil
	require.NoError(t, h.pass())
	require.Empty(t, mintedAccessors(h.fv))
	require.Empty(t, h.ledger())
	require.True(t, h.fv.valid(h.currentAccessor))
}

// An uncertain swap that never commits: a new operator process fences
// it from the ledger alone and revokes the minted token.
func TestUncertainSwapNeverCommitsAfterCrash(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.arm()
	h.onUpdate = func(n int, s *corev1.Secret) (error, bool) {
		if h.isSwap(s) {
			return timeout, true
		}
		return nil, false
	}
	require.Error(t, h.pass())
	require.Len(t, mintedAccessors(h.fv), 1, "kept while the outcome is unknown")
	h.delayed = nil // the write is lost

	h.m = h.newManager()
	h.onUpdate = nil
	h.fv.resetCalls()
	require.NoError(t, h.pass())
	require.Empty(t, mintedAccessors(h.fv))
	require.Len(t, h.fv.callsOf("auth/token/revoke-accessor"), 1)
	require.Equal(t, currentToken, h.token())
	require.Empty(t, h.ledger())
	require.True(t, h.fv.valid(h.currentAccessor))
}

// The person's case: the timed-out swap commits AFTER the fence's
// verification read. The fence write then conflicts, the re-read finds
// the swap landed, and the minted token — now the live one — survives.
func TestUncertainSwapCommitsAfterVerificationRead(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.arm()
	h.onUpdate = func(n int, s *corev1.Secret) (error, bool) {
		if h.isSwap(s) {
			return timeout, true
		}
		if h.isBackoffWrite(s) {
			// Lost too: nothing moves the Secret before the fence reads it.
			return timeout, false
		}
		return nil, false
	}
	require.Error(t, h.pass())
	minted := mintedAccessors(h.fv)
	require.Len(t, minted, 1)

	h.m = h.newManager()
	h.onUpdate = nil
	h.arm()
	// Get 1 is the pass's own read; Get 2 is the fence's verification read.
	h.commitAfterGet = 2
	h.fv.resetCalls()
	require.NoError(t, h.pass())

	require.Empty(t, h.fv.callsOf("auth/token/revoke-accessor"))
	require.Equal(t, minted[0], "acc-"+h.token(), "the swap landed")
	require.True(t, h.fv.valid(minted[0]))
	l := h.ledger()
	require.Len(t, l, 1)
	require.Equal(t, h.currentAccessor, l[0].Accessor)
	require.Equal(t, reasonSuperseded, l[0].Reason)
}

func TestUncertainFenceKeepsEverything(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.arm()
	h.onUpdate = func(n int, s *corev1.Secret) (error, bool) {
		if h.isSwap(s) {
			return timeout, false
		}
		return nil, false
	}
	require.Error(t, h.pass())

	h.onUpdate = func(int, *corev1.Secret) (error, bool) { return timeout, false }
	h.fv.resetCalls()
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/revoke-accessor"))
	require.Len(t, mintedAccessors(h.fv), 1)
	require.Equal(t, stateUnresolved, h.ledger()[0].State)
}

func TestFailedFenceReadKeepsMintedToken(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.arm()
	h.onUpdate = func(n int, s *corev1.Secret) (error, bool) {
		if h.isSwap(s) {
			return timeout, false
		}
		return nil, false
	}
	require.Error(t, h.pass())

	h.onUpdate = nil
	h.arm()
	h.onGet = func(n int) error {
		if n == 2 {
			return errors.New("apiserver unreachable")
		}
		return nil
	}
	h.fv.resetCalls()
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/revoke-accessor"))
	require.Len(t, mintedAccessors(h.fv), 1)
	require.Equal(t, stateUnresolved, h.ledger()[0].State)
}

// B's swap is uncertain; another writer installs C, which moves the
// Secret past B's precondition. B resolves as not landed and is revoked;
// A was replaced (by C, not B) so it stays scheduled and goes at its time.
func TestInterleavedWriterKeepsOldEntry(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.arm()
	h.onUpdate = func(n int, s *corev1.Secret) (error, bool) {
		if h.isSwap(s) {
			return timeout, false
		}
		return nil, false
	}
	require.Error(t, h.pass())
	b := mintedAccessors(h.fv)[0]
	h.onUpdate = nil

	h.fv.add("hvs.token-c", t0)
	h.setToken("hvs.token-c")
	require.NoError(t, h.pass())
	require.False(t, h.fv.valid(b))
	l := h.ledger()
	require.Len(t, l, 1)
	require.Equal(t, h.currentAccessor, l[0].Accessor)

	h.now = t0.Add(2 * time.Hour)
	require.NoError(t, h.pass())
	require.False(t, h.fv.valid(h.currentAccessor))
	require.True(t, h.fv.valid("acc-hvs.token-c"))
	require.Empty(t, h.ledger())
}

func TestNoMintWhileASwapIsUnresolved(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.seedLedger(LedgerEntry{Accessor: "acc-elsewhere", Reason: reasonUnresolvedMint,
		State: stateUnresolved, SwapID: "s0"})
	h.annotate(rotateNowAnnotation, "true")
	h.arm()
	h.onUpdate = func(int, *corev1.Secret) (error, bool) { return timeout, false } // the fence stays unresolved
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/create"))
}

func TestRepeatedSwapFailuresBackOff(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	h.onUpdate = func(n int, s *corev1.Secret) (error, bool) {
		if h.isSwap(s) {
			return conflict, false
		}
		return nil, false
	}
	for elapsed := time.Duration(0); elapsed <= 8*time.Minute; elapsed += 30 * time.Second {
		h.now = t0.Add(elapsed)
		_ = h.pass()
	}
	// Backoff 1m, 2m, 4m: mints at 0, 1m, 3m and 7m.
	require.Len(t, h.fv.callsOf("auth/token/create"), 4)
	require.Empty(t, mintedAccessors(h.fv), "every failed mint was revoked")
	require.Empty(t, h.ledger())
	require.Equal(t, currentToken, h.token())
}

func TestLedgerFullRefusesToMint(t *testing.T) {
	h := newHarness(t, time.Hour)
	var entries []LedgerEntry
	for i := 0; i < ledgerCap-1; i++ {
		entries = append(entries, LedgerEntry{Accessor: "acc-pending-" + string(rune('a'+i)),
			NotBefore: t0.Add(time.Hour), Reason: reasonSuperseded, State: stateScheduled, SwapID: "s"})
	}
	h.seedLedger(entries...)
	h.annotate(rotateNowAnnotation, "true")
	require.NoError(t, h.pass())
	require.Empty(t, h.fv.callsOf("auth/token/create"))
	require.True(t, hasEvent(h.drainEvents(), "TokenRotationSkipped"))
}

// The person's case: A rotates to B, B's backup fails, B dies during the
// grace window. Recovery must not reinstall A from the stale transit
// backup; self-heal installs C, and at the deadline A is revoked and C
// is untouched.
func TestRecoveryRefusesALedgeredBackup(t *testing.T) {
	h := newHarness(t, 800*time.Hour)
	require.NoError(t, h.pass())
	b := h.token()
	h.fv.invalidate("acc-" + b)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "vault-0", Namespace: "vault",
			Labels: map[string]string{"app.kubernetes.io/name": "vault"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	require.NoError(t, h.base.Create(context.Background(), pod))
	h.vtu.Spec.Initialization.TokenRecovery = vaultv1alpha1.TokenRecoverySpec{Enabled: true, BackupToTransit: true}
	h.m.AdminTokenBackup = staticBackup{token: currentToken}
	h.m.VaultFactory = stubFactory{api: h.fv.client()}

	require.False(t, h.m.tryRecoverFromTransitBackup(context.Background(), h.vtu))
	require.True(t, hasEvent(h.drainEvents(), "TokenRecoverySkipped"))
	require.Equal(t, b, h.token())

	// Self-heal installs C, keeping every other annotation.
	h.fv.add("hvs.token-c", t0)
	h.setToken("hvs.token-c")
	h.now = t0.Add(2 * time.Hour)
	require.NoError(t, h.pass())
	require.False(t, h.fv.valid(h.currentAccessor))
	require.True(t, h.fv.valid("acc-hvs.token-c"))
	require.Empty(t, h.ledger())
}

func TestRenewReadsTheSecretUncached(t *testing.T) {
	h := newHarness(t, time.Hour)
	stale := interceptor.NewClient(h.base, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("cached read used")
		},
	})
	h.m.Client = stale
	h.m.APIReader = h.c
	require.NoError(t, h.m.RenewIfNeeded(context.Background(), h.vtu, h.fv.client()))
	require.NotEmpty(t, h.fv.callsOf("auth/token/lookup-self"))
}

type staticBackup struct{ token string }

func (s staticBackup) Backup(context.Context, *vaultv1alpha1.VaultTransitUnseal, string) error {
	return errors.New("transit unavailable")
}

func (s staticBackup) Recover(context.Context, *vaultv1alpha1.VaultTransitUnseal) (string, error) {
	return s.token, nil
}

type stubFactory struct{ api *vaultapi.Client }

func (f stubFactory) NewClientForPod(context.Context, *corev1.Pod, *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error) {
	return stubClient{api: f.api}, nil
}

type stubClient struct {
	vault.Client
	api *vaultapi.Client
}

func (c stubClient) GetAPIClient() *vaultapi.Client { return c.api }

// gauge reads a registered gauge's current value.
func gauge(t *testing.T, name string) float64 {
	t.Helper()
	_ = metrics.NewRecorder()
	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s not registered", name)
	return 0
}
