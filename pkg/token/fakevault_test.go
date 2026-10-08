package token

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/require"
)

// fakeVault is a stateful stand-in for Vault's token endpoints. It keeps
// tokens by value and accessor, and records every call so a test can
// assert exactly what the operator asked Vault to do.
type fakeVault struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	tokens   map[string]*fakeToken // by token value
	accessor map[string]*fakeToken // by accessor
	calls    []string
	next     int

	// lookupSelfFails makes lookup-self answer 500 (a timeout-like error
	// on a token that is still valid).
	lookupSelfFails bool
	// revokeIsNoop makes revoke-accessor answer success but keep the
	// token, so the verification lookup still finds it.
	revokeIsNoop bool
	// createFails makes token creation answer 500.
	createFails bool
}

type fakeToken struct {
	value    string
	accessor string
	created  time.Time
}

func newFakeVault(t *testing.T) *fakeVault {
	t.Helper()
	fv := &fakeVault{
		t:        t,
		tokens:   map[string]*fakeToken{},
		accessor: map[string]*fakeToken{},
	}
	fv.srv = httptest.NewServer(http.HandlerFunc(fv.serve))
	t.Cleanup(fv.srv.Close)
	return fv
}

// add registers a valid token and returns its accessor.
func (fv *fakeVault) add(value string, created time.Time) string {
	fv.mu.Lock()
	defer fv.mu.Unlock()
	fv.next++
	tok := &fakeToken{value: value, accessor: fmt.Sprintf("acc-%s", value), created: created}
	fv.tokens[value] = tok
	fv.accessor[tok.accessor] = tok
	return tok.accessor
}

func (fv *fakeVault) valid(accessor string) bool {
	fv.mu.Lock()
	defer fv.mu.Unlock()
	_, ok := fv.accessor[accessor]
	return ok
}

// invalidate makes a token unknown to Vault, as an expiry would.
func (fv *fakeVault) invalidate(accessor string) {
	fv.mu.Lock()
	defer fv.mu.Unlock()
	if tok, ok := fv.accessor[accessor]; ok {
		delete(fv.tokens, tok.value)
		delete(fv.accessor, accessor)
	}
}

// callsOf returns the recorded calls whose path ends with suffix.
func (fv *fakeVault) callsOf(suffix string) []string {
	fv.mu.Lock()
	defer fv.mu.Unlock()
	var out []string
	for _, c := range fv.calls {
		if strings.HasSuffix(strings.SplitN(c, " ", 2)[0], suffix) {
			out = append(out, c)
		}
	}
	return out
}

func (fv *fakeVault) resetCalls() {
	fv.mu.Lock()
	defer fv.mu.Unlock()
	fv.calls = nil
}

func (fv *fakeVault) client() *vaultapi.Client {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = fv.srv.URL
	cfg.MaxRetries = 0
	c, err := vaultapi.NewClient(cfg)
	require.NoError(fv.t, err)
	return c
}

func (fv *fakeVault) serve(w http.ResponseWriter, r *http.Request) {
	fv.mu.Lock()
	defer fv.mu.Unlock()

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	arg, _ := body["accessor"].(string)
	fv.calls = append(fv.calls, strings.TrimSpace(path+" "+arg))

	caller, callerOK := fv.tokens[r.Header.Get("X-Vault-Token")]
	errorf := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{msg}})
	}
	reply := func(v map[string]any) {
		_ = json.NewEncoder(w).Encode(v)
	}

	switch path {
	case "auth/token/lookup-self":
		if fv.lookupSelfFails {
			errorf(http.StatusInternalServerError, "context deadline exceeded")
			return
		}
		if !callerOK {
			errorf(http.StatusForbidden, "permission denied")
			return
		}
		reply(map[string]any{"data": map[string]any{
			"accessor":      caller.accessor,
			"creation_time": caller.created.Unix(),
			"ttl":           604800,
			"renewable":     true,
		}})
	case "auth/token/create":
		if !callerOK {
			errorf(http.StatusForbidden, "permission denied")
			return
		}
		if fv.createFails {
			errorf(http.StatusInternalServerError, "internal error")
			return
		}
		fv.next++
		value := fmt.Sprintf("hvs.minted-%d", fv.next)
		tok := &fakeToken{value: value, accessor: "acc-" + value, created: time.Now()}
		fv.tokens[value] = tok
		fv.accessor[tok.accessor] = tok
		reply(map[string]any{"auth": map[string]any{
			"client_token": value,
			"accessor":     tok.accessor,
			"renewable":    true,
		}})
	case "auth/token/revoke-accessor":
		if !callerOK {
			errorf(http.StatusForbidden, "permission denied")
			return
		}
		tok, ok := fv.accessor[arg]
		if !ok {
			errorf(http.StatusBadRequest, "1 error occurred:\n\t* invalid accessor\n\n")
			return
		}
		if !fv.revokeIsNoop {
			delete(fv.tokens, tok.value)
			delete(fv.accessor, arg)
		}
		w.WriteHeader(http.StatusNoContent)
	case "auth/token/lookup-accessor":
		if !callerOK {
			errorf(http.StatusForbidden, "permission denied")
			return
		}
		tok, ok := fv.accessor[arg]
		if !ok {
			errorf(http.StatusBadRequest, "1 error occurred:\n\t* invalid accessor\n\n")
			return
		}
		reply(map[string]any{"data": map[string]any{"accessor": tok.accessor}})
	default:
		errorf(http.StatusNotFound, "unsupported path "+path)
	}
}
