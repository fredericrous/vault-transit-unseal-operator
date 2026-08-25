package reconciler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/reconciler"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// storedKeyVault is a fake Shamir-sealed Vault: it answers sys/unseal, counts
// accepted shares toward a threshold, and reports its seal state.
type storedKeyVault struct {
	*httptest.Server

	mu        sync.Mutex
	threshold int
	accepted  map[string]bool
	sealed    bool
	progress  int

	SubmittedKeys []string
}

func newStoredKeyVault(threshold int, shares ...string) *storedKeyVault {
	v := &storedKeyVault{
		threshold: threshold,
		accepted:  make(map[string]bool, len(shares)),
		sealed:    true,
	}
	for _, s := range shares {
		v.accepted[s] = true
	}

	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sys/unseal" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		v.mu.Lock()
		defer v.mu.Unlock()

		var body struct {
			Key   string `json:"key"`
			Reset bool   `json:"reset"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Reset {
			v.progress = 0
		} else {
			v.SubmittedKeys = append(v.SubmittedKeys, body.Key)
			if !v.accepted[body.Key] {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"errors":["failed to parse key as base64"]}`)
				return
			}
			v.progress++
			if v.progress >= v.threshold {
				v.sealed = false
				v.progress = 0
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"type":"shamir","initialized":true,"sealed":%t,"t":%d,"n":5,"progress":%d}`,
			v.sealed, v.threshold, v.progress)
	}))

	return v
}

func (v *storedKeyVault) isSealed() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.sealed
}

// storedKeyVaultClient is a vault.Client whose API client points at a fake
// Vault, so the reconciler's unseal path exercises real HTTP.
type storedKeyVaultClient struct {
	initialized bool
	sealed      bool
	address     string
	initCalls   int
}

func (c *storedKeyVaultClient) CheckStatus(context.Context) (*vault.Status, error) {
	return &vault.Status{Initialized: c.initialized, Sealed: c.sealed}, nil
}

func (c *storedKeyVaultClient) Initialize(context.Context, *vault.InitRequest) (*vault.InitResponse, error) {
	c.initCalls++
	return &vault.InitResponse{RootToken: "root", RecoveryKeysB64: []string{"k"}}, nil
}

func (c *storedKeyVaultClient) IsHealthy(context.Context) bool { return true }

func (c *storedKeyVaultClient) GetAPIClient() *vaultapi.Client {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = c.address
	cfg.MaxRetries = 0
	client, _ := vaultapi.NewClient(cfg)
	return client
}

func (c *storedKeyVaultClient) EnableAuth(context.Context, string, string) error { return nil }
func (c *storedKeyVaultClient) AuthEnabled(context.Context, string) (bool, error) {
	return false, nil
}

func (c *storedKeyVaultClient) WriteAuth(context.Context, string, map[string]interface{}) error {
	return nil
}
func (c *storedKeyVaultClient) MountExists(context.Context, string) (bool, error) { return false, nil }
func (c *storedKeyVaultClient) MountSecretEngine(context.Context, string, *vault.MountInput) error {
	return nil
}
func (c *storedKeyVaultClient) WritePolicy(context.Context, string, string) error { return nil }

type storedKeyFactory struct {
	client *storedKeyVaultClient
}

func (f *storedKeyFactory) NewClientForPod(context.Context, *corev1.Pod, *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error) {
	return f.client, nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type storedKeyHarness struct {
	reconciler *reconciler.VaultReconciler
	vtu        *vaultv1alpha1.VaultTransitUnseal
	pod        *corev1.Pod
	vaultAPI   *storedKeyVault
	client     *storedKeyVaultClient
	secrets    *mockSecretManager
	metrics    *mockMetricsRecorder
	events     *record.FakeRecorder
	k8s        ctrlclient.Client
}

type harnessOptions struct {
	mode        vaultv1alpha1.UnsealMode
	initialized bool
	sealed      bool
	threshold   int
	shares      []string
	secretValue *string // nil means the Secret is absent
	// podRunningFor backdates the vault container's start so a transit-mode
	// test can get past the boot grace.
	podRunningFor time.Duration
}

func newStoredKeyHarness(t *testing.T, opts harnessOptions) *storedKeyHarness {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, vaultv1alpha1.AddToScheme(scheme))

	fakeVault := newStoredKeyVault(opts.threshold, opts.shares...)
	t.Cleanup(fakeVault.Close)

	pod := createReadyPod("vault-0", "vault", map[string]string{"app": "vault"})
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.NewTime(time.Now().Add(-opts.podRunningFor)),
		},
	}

	vtu := &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{Name: "vault", Namespace: "vault"},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			Mode: opts.mode,
			VaultPod: vaultv1alpha1.VaultPodSpec{
				Namespace: "vault",
				Selector:  map[string]string{"app": "vault"},
			},
			TransitVault: vaultv1alpha1.TransitVaultSpec{
				SecretRef: vaultv1alpha1.SecretReference{Name: "transit-token", Key: "token"},
			},
			Monitoring: vaultv1alpha1.MonitoringSpec{CheckInterval: "30s"},
		},
	}

	secrets := newMockSecretManager()
	if opts.secretValue != nil {
		secrets.secrets["vault/vault-unseal-keys"] = map[string][]byte{
			"unseal-keys.txt": []byte(*opts.secretValue),
		}
	}
	// Transit mode always needs its token; harmless in stored-key mode.
	secrets.secrets["vault/transit-token"] = map[string][]byte{"token": []byte("t")}

	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, vtu).Build()
	vaultClient := &storedKeyVaultClient{
		initialized: opts.initialized,
		sealed:      opts.sealed,
		address:     fakeVault.URL,
	}
	metrics := &mockMetricsRecorder{}
	events := record.NewFakeRecorder(100)

	return &storedKeyHarness{
		reconciler: &reconciler.VaultReconciler{
			Client:          k8s,
			Log:             logr.Discard(),
			Recorder:        events,
			VaultFactory:    &storedKeyFactory{client: vaultClient},
			SecretManager:   secrets,
			MetricsRecorder: metrics,
		},
		vtu:      vtu,
		pod:      pod,
		vaultAPI: fakeVault,
		client:   vaultClient,
		secrets:  secrets,
		metrics:  metrics,
		events:   events,
		k8s:      k8s,
	}
}

func (h *storedKeyHarness) condition(t *testing.T, condType string) *vaultv1alpha1.Condition {
	t.Helper()
	for i := range h.vtu.Status.Conditions {
		if h.vtu.Status.Conditions[i].Type == condType {
			return &h.vtu.Status.Conditions[i]
		}
	}
	return nil
}

func (h *storedKeyHarness) drainEvents() []string {
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

func (h *storedKeyHarness) podExists(t *testing.T) bool {
	t.Helper()
	var pod corev1.Pod
	err := h.k8s.Get(context.Background(), types.NamespacedName{Namespace: "vault", Name: "vault-0"}, &pod)
	if apierrors.IsNotFound(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func ptr(s string) *string { return &s }

func containsEvent(events []string, reason string) bool {
	for _, e := range events {
		if strings.Contains(e, reason) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestStoredKeyUnsealsSealedVault(t *testing.T) {
	h := newStoredKeyHarness(t, harnessOptions{
		mode:        vaultv1alpha1.UnsealModeStoredKey,
		initialized: true,
		sealed:      true,
		threshold:   1,
		shares:      []string{"share-one"},
		secretValue: ptr("share-one\n"),
	})

	require.NoError(t, h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu))

	assert.False(t, h.vaultAPI.isSealed(), "vault should be unsealed")
	assert.Equal(t, []string{"share-one"}, h.vaultAPI.SubmittedKeys)

	// A Shamir vault unseals in place — nothing is restarted.
	assert.True(t, h.podExists(t), "stored-key mode must not delete the pod")

	assert.Equal(t, []unsealAttemptMetric{{"stored-key", true}}, h.metrics.unsealAttempts)

	ready := h.condition(t, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, string(metav1.ConditionTrue), ready.Status)

	present := h.condition(t, "KeySecretPresent")
	require.NotNil(t, present)
	assert.Equal(t, string(metav1.ConditionTrue), present.Status)
	assert.Equal(t, "SharesAvailable", present.Reason)

	assert.NotEmpty(t, h.vtu.Status.LastUnsealTime)
	assert.False(t, h.vtu.Status.Sealed)

	assert.True(t, containsEvent(h.drainEvents(), "Unsealed"))
}

func TestStoredKeyUnsealsWithThresholdGreaterThanOne(t *testing.T) {
	h := newStoredKeyHarness(t, harnessOptions{
		mode:        vaultv1alpha1.UnsealModeStoredKey,
		initialized: true,
		sealed:      true,
		threshold:   3,
		shares:      []string{"a", "b", "c", "d", "e"},
		secretValue: ptr("a\r\nb\r\nc\r\nd\r\ne\r\n"),
	})

	require.NoError(t, h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu))

	assert.False(t, h.vaultAPI.isSealed())
	// Stops at the threshold; the spare shares stay unspent.
	assert.Equal(t, []string{"a", "b", "c"}, h.vaultAPI.SubmittedKeys)
	assert.Equal(t, []unsealAttemptMetric{{"stored-key", true}}, h.metrics.unsealAttempts)
}

func TestStoredKeyMissingSecret(t *testing.T) {
	h := newStoredKeyHarness(t, harnessOptions{
		mode:        vaultv1alpha1.UnsealModeStoredKey,
		initialized: true,
		sealed:      true,
		threshold:   1,
		shares:      []string{"share-one"},
		secretValue: nil,
	})

	err := h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu)

	require.Error(t, err)
	assert.True(t, h.vaultAPI.isSealed())
	assert.Empty(t, h.vaultAPI.SubmittedKeys)
	assert.Equal(t, []unsealAttemptMetric{{"stored-key", false}}, h.metrics.unsealAttempts)

	present := h.condition(t, "KeySecretPresent")
	require.NotNil(t, present)
	assert.Equal(t, string(metav1.ConditionFalse), present.Status)

	assert.True(t, containsEvent(h.drainEvents(), "UnsealKeyUnavailable"))
}

func TestStoredKeyEmptySecretValue(t *testing.T) {
	h := newStoredKeyHarness(t, harnessOptions{
		mode:        vaultv1alpha1.UnsealModeStoredKey,
		initialized: true,
		sealed:      true,
		threshold:   1,
		shares:      []string{"share-one"},
		secretValue: ptr("\n  \n"),
	})

	require.Error(t, h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu))

	present := h.condition(t, "KeySecretPresent")
	require.NotNil(t, present)
	assert.Equal(t, "NoShares", present.Reason)
}

func TestStoredKeyWrongShareFailsWithoutLeakingIt(t *testing.T) {
	const share = "TGDF3s0iA8oJT9tvbYyBvbLMhFqQO/1yDzrxRZWEmvE="

	h := newStoredKeyHarness(t, harnessOptions{
		mode:        vaultv1alpha1.UnsealModeStoredKey,
		initialized: true,
		sealed:      true,
		threshold:   1,
		shares:      []string{"the-real-share"},
		secretValue: ptr(share + "\n"),
	})

	err := h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), share)
	assert.Equal(t, []unsealAttemptMetric{{"stored-key", false}}, h.metrics.unsealAttempts)

	ready := h.condition(t, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, string(metav1.ConditionFalse), ready.Status)
	assert.Equal(t, "UnsealFailed", ready.Reason)
	// The condition message lands in `kubectl describe`; it must not carry
	// the share either.
	assert.NotContains(t, ready.Message, share)

	for _, e := range h.drainEvents() {
		assert.NotContains(t, e, share)
	}
}

// TestStoredKeyNeverInitializes is the safety property of requirement 4: an
// uninitialized Vault is reported, not initialized. Initializing on its own
// would mint shares nobody captured — and would "initialize" a Vault that only
// looks empty because its storage came up wrong.
func TestStoredKeyNeverInitializes(t *testing.T) {
	h := newStoredKeyHarness(t, harnessOptions{
		mode:        vaultv1alpha1.UnsealModeStoredKey,
		initialized: false,
		sealed:      true,
		threshold:   1,
		shares:      []string{"share-one"},
		secretValue: ptr("share-one\n"),
	})

	require.NoError(t, h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu))

	assert.Zero(t, h.client.initCalls, "stored-key mode must never call sys/init")
	assert.Empty(t, h.vaultAPI.SubmittedKeys, "an uninitialized vault must not be unsealed")
	assert.Empty(t, h.metrics.initializations)

	initialized := h.condition(t, "Initialized")
	require.NotNil(t, initialized)
	assert.Equal(t, string(metav1.ConditionFalse), initialized.Status)
	assert.Equal(t, "AwaitingExternalInit", initialized.Reason)
	assert.Contains(t, initialized.Message, "vault-setup")

	ready := h.condition(t, "Ready")
	require.NotNil(t, ready, "Ready must be reported even on the path that returns early")
	assert.Equal(t, string(metav1.ConditionFalse), ready.Status)

	assert.True(t, containsEvent(h.drainEvents(), "InitializationDeferred"))
}

// TestTransitModeStillRestartsSealedPod is the regression guard: transit mode
// must behave exactly as it did before stored-key mode existed — restart the
// pod, never POST to sys/unseal.
func TestTransitModeStillRestartsSealedPod(t *testing.T) {
	for _, mode := range []vaultv1alpha1.UnsealMode{"", vaultv1alpha1.UnsealModeTransit} {
		t.Run(fmt.Sprintf("mode=%q", mode), func(t *testing.T) {
			h := newStoredKeyHarness(t, harnessOptions{
				mode:          mode,
				initialized:   true,
				sealed:        true,
				threshold:     1,
				shares:        []string{"share-one"},
				secretValue:   ptr("share-one\n"),
				podRunningFor: 10 * time.Minute, // past the boot grace
			})

			require.NoError(t, h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu))

			assert.False(t, h.podExists(t), "transit mode recovers by restarting the pod")
			assert.Empty(t, h.vaultAPI.SubmittedKeys, "transit mode must never POST sys/unseal")
			assert.Empty(t, h.metrics.unsealAttempts, "the unseal metric belongs to stored-key mode")
			assert.Nil(t, h.condition(t, "KeySecretPresent"), "transit mode has no key secret")

			assert.True(t, containsEvent(h.drainEvents(), "RestartedSealedPod"))
		})
	}
}

func TestTransitModeStillInitializes(t *testing.T) {
	h := newStoredKeyHarness(t, harnessOptions{
		mode:        vaultv1alpha1.UnsealModeTransit,
		initialized: false,
		sealed:      true,
		threshold:   1,
		secretValue: nil,
	})

	require.NoError(t, h.reconciler.ProcessPod(context.Background(), h.pod, h.vtu))

	assert.Equal(t, 1, h.client.initCalls)
	assert.Equal(t, []bool{true}, h.metrics.initializations)
}

// TestReconcileValidatesPerMode checks the credential gate at the top of
// Reconcile: stored-key mode must not demand a transit token, and must refuse
// to proceed without its own key Secret.
func TestReconcileValidatesPerMode(t *testing.T) {
	t.Run("stored-key does not require a transit token", func(t *testing.T) {
		h := newStoredKeyHarness(t, harnessOptions{
			mode:        vaultv1alpha1.UnsealModeStoredKey,
			initialized: true,
			sealed:      true,
			threshold:   1,
			shares:      []string{"share-one"},
			secretValue: ptr("share-one\n"),
		})
		delete(h.secrets.secrets, "vault/transit-token")

		result := h.reconciler.Reconcile(context.Background(), h.vtu)

		assert.NoError(t, result.Error)
		assert.Equal(t, "stored-key", h.vtu.Status.UnsealMode)
	})

	t.Run("stored-key refuses to run without its key secret", func(t *testing.T) {
		h := newStoredKeyHarness(t, harnessOptions{
			mode:        vaultv1alpha1.UnsealModeStoredKey,
			initialized: true,
			sealed:      true,
			threshold:   1,
			shares:      []string{"share-one"},
			secretValue: nil,
		})

		result := h.reconciler.Reconcile(context.Background(), h.vtu)

		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "unseal key secret validation failed")
	})

	t.Run("transit still requires its token", func(t *testing.T) {
		h := newStoredKeyHarness(t, harnessOptions{
			mode:        vaultv1alpha1.UnsealModeTransit,
			initialized: true,
			sealed:      false,
			threshold:   1,
		})
		delete(h.secrets.secrets, "vault/transit-token")

		result := h.reconciler.Reconcile(context.Background(), h.vtu)

		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "transit token validation failed")
		assert.Equal(t, "transit", h.vtu.Status.UnsealMode)
	})
}

func TestStatusUnsealModeReflectsAbsentMode(t *testing.T) {
	h := newStoredKeyHarness(t, harnessOptions{
		mode:        "",
		initialized: true,
		sealed:      false,
		threshold:   1,
	})

	h.reconciler.Reconcile(context.Background(), h.vtu)

	assert.Equal(t, "transit", h.vtu.Status.UnsealMode)
}
