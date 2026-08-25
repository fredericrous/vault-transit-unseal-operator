// Package storedkey implements stored-key (Shamir) unsealing: reading unseal
// key shares out of a Kubernetes Secret and submitting them to Vault's
// sys/unseal endpoint.
//
// Nothing in this package ever formats a share into a log line, an error
// message, an event, or a status condition. Callers must keep that property:
// the share is the entire security boundary of a root-of-trust Vault.
package storedkey

import "strings"

// ParseKeys extracts unseal key shares from the raw value of the Secret key.
//
// The format is one share per line. Blank lines are dropped and each share is
// stripped of surrounding whitespace and of any carriage return, so a file
// written on Windows, or with a trailing newline, or with no newline at all,
// all parse identically.
//
// This is a strict superset of the vault-auto-unseal CronJob it replaces. That
// script did `tr -d '\n\r' < unseal-keys.txt` on a file whose format is
// documented as exactly one key, so for a one-share Secret ParseKeys returns
// precisely the string the CronJob would have POSTed. Where the CronJob would
// silently CONCATENATE a multi-share file into one garbage key, ParseKeys
// returns the shares separately and the unsealer submits them in turn.
func ParseKeys(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	keys := make([]string, 0, len(lines))

	for _, line := range lines {
		key := strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
		if key == "" {
			continue
		}
		keys = append(keys, key)
	}

	return keys
}
