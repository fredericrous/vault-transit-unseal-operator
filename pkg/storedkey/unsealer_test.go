package storedkey

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVault is a minimal stand-in for a Shamir-sealed Vault's sys/unseal
// endpoint, faithful to the parts that matter: shares accumulate toward a
// threshold, a `reset` clears that progress, and an unknown share is rejected
// with a 400 the way Vault rejects one.
type fakeVault struct {
	*httptest.Server

	mu sync.Mutex

	threshold int
	shares    map[string]bool // shares Vault will accept
	sealed    bool
	progress  int

	// SubmittedKeys records every share the client sent, so a test can
	// assert on ORDER and COUNT without the production code ever having to
	// hand key material back.
	SubmittedKeys []string
	Resets        int

	// httpStatus, when non-zero, is returned for every unseal call.
	httpStatus int
}

func newFakeVault(threshold int, shares ...string) *fakeVault {
	f := &fakeVault{
		threshold: threshold,
		shares:    make(map[string]bool, len(shares)),
		sealed:    true,
	}
	for _, s := range shares {
		f.shares[s] = true
	}

	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sys/unseal" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.handleUnseal(w, r)
	}))

	return f
}

func (f *fakeVault) handleUnseal(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var body struct {
		Key   string `json:"key"`
		Reset bool   `json:"reset"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if body.Reset {
		f.Resets++
		f.progress = 0
		f.writeStatus(w)
		return
	}

	f.SubmittedKeys = append(f.SubmittedKeys, body.Key)

	if f.httpStatus != 0 {
		w.WriteHeader(f.httpStatus)
		_, _ = fmt.Fprint(w, `{"errors":["vault is temporarily unavailable"]}`)
		return
	}

	if !f.shares[body.Key] {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"errors":["failed to parse key as base64: illegal base64 data"]}`)
		return
	}

	f.progress++
	if f.progress >= f.threshold {
		f.sealed = false
		f.progress = 0
	}
	f.writeStatus(w)
}

func (f *fakeVault) writeStatus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"type":"shamir","initialized":true,"sealed":%t,"t":%d,"n":5,"progress":%d}`,
		f.sealed, f.threshold, f.progress)
}

func (f *fakeVault) client(t *testing.T) *vaultapi.Client {
	t.Helper()

	cfg := vaultapi.DefaultConfig()
	cfg.Address = f.URL
	// The real client keeps the library's retry-on-5xx; here it would only
	// add seconds of backoff to the error-path assertions.
	cfg.MaxRetries = 0
	client, err := vaultapi.NewClient(cfg)
	require.NoError(t, err)

	return client
}

func TestUnsealSingleShare(t *testing.T) {
	fake := newFakeVault(1, "share-one")
	defer fake.Close()

	result, err := Unseal(context.Background(), fake.client(t), []string{"share-one"})

	require.NoError(t, err)
	assert.False(t, result.Sealed)
	assert.Equal(t, 1, result.SharesSubmitted)
	assert.Equal(t, []string{"share-one"}, fake.SubmittedKeys)
}

func TestUnsealThresholdGreaterThanOne(t *testing.T) {
	fake := newFakeVault(3, "a", "b", "c", "d")
	defer fake.Close()

	result, err := Unseal(context.Background(), fake.client(t), []string{"a", "b", "c", "d"})

	require.NoError(t, err)
	assert.False(t, result.Sealed)
	// Stops the moment Vault reports unsealed: the fourth share is spare
	// and must not be spent.
	assert.Equal(t, 3, result.SharesSubmitted)
	assert.Equal(t, []string{"a", "b", "c"}, fake.SubmittedKeys)
}

func TestUnsealReportsProgressWhileSealed(t *testing.T) {
	fake := newFakeVault(3, "a", "b", "c")
	defer fake.Close()

	// Only two of the three required shares are in the Secret.
	result, err := Unseal(context.Background(), fake.client(t), []string{"a", "b"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "still sealed")
	assert.True(t, result.Sealed)
	assert.Equal(t, 2, result.SharesSubmitted)
	assert.Equal(t, 2, result.Progress)
	assert.Equal(t, 3, result.Threshold)
	// Progress is left standing on purpose: nothing was rejected, so the
	// next reconcile can top it up rather than start over.
	assert.Equal(t, 0, fake.Resets)
}

func TestUnsealRejectedShareResetsProgress(t *testing.T) {
	fake := newFakeVault(3, "a", "b", "c")
	defer fake.Close()

	result, err := Unseal(context.Background(), fake.client(t), []string{"a", "bogus", "c"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "submitting share 2 of 3")
	assert.True(t, result.Sealed)
	// The half-finished attempt is abandoned so the next one starts clean
	// instead of stacking a second partial unseal on a stale one.
	assert.Equal(t, 1, fake.Resets)
	assert.Equal(t, 0, result.Progress)
}

func TestUnsealFirstShareRejectedDoesNotReset(t *testing.T) {
	fake := newFakeVault(1, "share-one")
	defer fake.Close()

	_, err := Unseal(context.Background(), fake.client(t), []string{"bogus"})

	require.Error(t, err)
	// Nothing was accepted, so there is no progress to abandon.
	assert.Equal(t, 0, fake.Resets)
}

func TestUnsealServerError(t *testing.T) {
	fake := newFakeVault(1, "share-one")
	fake.httpStatus = http.StatusInternalServerError
	defer fake.Close()

	_, err := Unseal(context.Background(), fake.client(t), []string{"share-one"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sys/unseal")
}

// TestUnsealErrorsNeverCarryKeyMaterial is the security oracle: the share is
// the whole security boundary of a root-of-trust Vault, and an error string
// ends up in operator logs, Kubernetes events, and CR status conditions.
func TestUnsealErrorsNeverCarryKeyMaterial(t *testing.T) {
	const secret = "TGDF3s0iA8oJT9tvbYyBvbLMhFqQO/1yDzrxRZWEmvE="

	t.Run("rejected share", func(t *testing.T) {
		fake := newFakeVault(1, "different-share")
		defer fake.Close()

		_, err := Unseal(context.Background(), fake.client(t), []string{secret})

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	})

	t.Run("server error", func(t *testing.T) {
		fake := newFakeVault(1, secret)
		fake.httpStatus = http.StatusInternalServerError
		defer fake.Close()

		_, err := Unseal(context.Background(), fake.client(t), []string{secret})

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	})

	t.Run("threshold not met", func(t *testing.T) {
		fake := newFakeVault(3, secret, "b")
		defer fake.Close()

		result, err := Unseal(context.Background(), fake.client(t), []string{secret, "b"})

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.NotContains(t, fmt.Sprintf("%+v", result), secret)
	})
}

func TestUnsealNoKeys(t *testing.T) {
	fake := newFakeVault(1, "share-one")
	defer fake.Close()

	_, err := Unseal(context.Background(), fake.client(t), nil)

	assert.ErrorIs(t, err, ErrNoKeys)
	assert.Empty(t, fake.SubmittedKeys)
}

func TestUnsealNilClient(t *testing.T) {
	_, err := Unseal(context.Background(), nil, []string{"share-one"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// TestUnsealParsedFromSecret walks the whole stored-key path the way the
// reconciler does: raw Secret bytes in, unsealed Vault out.
func TestUnsealParsedFromSecret(t *testing.T) {
	fake := newFakeVault(2, "share-one", "share-two")
	defer fake.Close()

	raw := []byte("share-one\r\nshare-two\r\n")

	result, err := Unseal(context.Background(), fake.client(t), ParseKeys(raw))

	require.NoError(t, err)
	assert.False(t, result.Sealed)
	assert.Equal(t, 2, result.SharesSubmitted)
	for _, submitted := range fake.SubmittedKeys {
		assert.False(t, strings.ContainsAny(submitted, "\r\n"), "line endings must be stripped before POSTing")
	}
}
