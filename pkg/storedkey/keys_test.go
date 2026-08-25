package storedkey

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseKeys(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{
			// The exact shape the vault-auto-unseal CronJob's Secret has
			// today: one key, written with `echo`, so a trailing newline.
			name: "single share with trailing newline",
			data: "share-one\n",
			want: []string{"share-one"},
		},
		{
			name: "single share with no trailing newline",
			data: "share-one",
			want: []string{"share-one"},
		},
		{
			name: "single share with CRLF",
			data: "share-one\r\n",
			want: []string{"share-one"},
		},
		{
			name: "three shares, one per line",
			data: "share-one\nshare-two\nshare-three\n",
			want: []string{"share-one", "share-two", "share-three"},
		},
		{
			name: "three shares with CRLF endings",
			data: "share-one\r\nshare-two\r\nshare-three\r\n",
			want: []string{"share-one", "share-two", "share-three"},
		},
		{
			name: "blank and whitespace-only lines are dropped",
			data: "share-one\n\n   \nshare-two\n\n",
			want: []string{"share-one", "share-two"},
		},
		{
			name: "surrounding whitespace is trimmed",
			data: "  share-one  \n\tshare-two\t\n",
			want: []string{"share-one", "share-two"},
		},
		{
			name: "empty value yields no shares",
			data: "",
			want: []string{},
		},
		{
			name: "whitespace-only value yields no shares",
			data: "\n\r\n   \n",
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ParseKeys([]byte(tt.data)))
		})
	}
}

// TestParseKeysMatchesCronJobForOneShare is the compatibility oracle for the
// swap. The CronJob this mode replaces did `tr -d '\n\r' < unseal-keys.txt`
// on a file documented as holding exactly one key. For any such file, ParseKeys
// must produce that identical string — otherwise swapping the CronJob for a CR
// would silently start POSTing a different key to sys/unseal.
func TestParseKeysMatchesCronJobForOneShare(t *testing.T) {
	const share = "TGDF3s0iA8oJT9tvbYyBvbLMhFqQO/1yDzrxRZWEmvE="

	for _, raw := range []string{share, share + "\n", share + "\r\n", share + "\n\n"} {
		// `tr -d '\n\r'`, verbatim.
		cronJobKey := strings.NewReplacer("\n", "", "\r", "").Replace(raw)

		keys := ParseKeys([]byte(raw))
		assert.Len(t, keys, 1)
		assert.Equal(t, cronJobKey, keys[0])
	}
}
