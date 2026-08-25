package v1alpha1

// EffectiveUnsealMode resolves spec.mode to the mode the operator will act in.
//
// The zero value matters here: resources written before this field existed
// have no `mode` at all, and CRD defaulting only fills it in on the next
// write. Anything that is not exactly "stored-key" therefore resolves to
// transit, which is the behaviour those resources already had. The CRD enum
// rejects unknown values at admission, so this is a belt-and-braces fallback
// rather than the real validation.
func (s *VaultTransitUnsealSpec) EffectiveUnsealMode() UnsealMode {
	if s.Mode == UnsealModeStoredKey {
		return UnsealModeStoredKey
	}
	return UnsealModeTransit
}

// IsStoredKeyMode reports whether this resource unseals from a stored key.
func (v *VaultTransitUnseal) IsStoredKeyMode() bool {
	return v.Spec.EffectiveUnsealMode() == UnsealModeStoredKey
}

// StoredKeySecretRef resolves the Secret holding the unseal key share(s),
// filling in the defaults that match the CronJob this mode replaces. Safe to
// call with spec.storedKey absent.
func (s *VaultTransitUnsealSpec) StoredKeySecretRef() (namespace, name, key string) {
	name = DefaultStoredKeySecretName
	key = DefaultStoredKeySecretKey
	namespace = s.VaultPod.Namespace

	if s.StoredKey != nil {
		if s.StoredKey.SecretRef.Name != "" {
			name = s.StoredKey.SecretRef.Name
		}
		if s.StoredKey.SecretRef.Key != "" {
			key = s.StoredKey.SecretRef.Key
		}
		if s.StoredKey.SecretRef.Namespace != "" {
			namespace = s.StoredKey.SecretRef.Namespace
		}
	}

	return namespace, name, key
}
