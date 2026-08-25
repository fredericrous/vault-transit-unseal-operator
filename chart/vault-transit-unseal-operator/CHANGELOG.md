# Changelog

All notable changes to the Vault Transit Unseal Operator Helm chart will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- **kube-rbac-proxy image no longer pulls.** The default was
  `gcr.io/kubebuilder/kube-rbac-proxy:v0.13.1`, a path kubebuilder has retired;
  it now 404s, so on any cluster without it cached the operator pod sat at
  **1/2 with ErrImagePull** — the manager container healthy, only the sidecar
  failing. Default is now
  `registry.k8s.io/kubebuilder/kube-rbac-proxy:v0.16.0`. Anyone who pinned an
  override (homelab already did) is unaffected.

### Notes
- **kube-rbac-proxy is deprecated upstream.** kubebuilder has dropped it in
  favour of controller-runtime's own metrics filter
  (`metricsserver.Options{SecureServing: true, FilterProvider:
  filters.WithAuthenticationAndAuthorization}`), which removes the sidecar
  entirely. This chart deliberately does NOT switch yet: the manager does not
  support it today — it takes no `--metrics-secure` flag and passes no `Metrics`
  option to `ctrl.NewManager` at all — so the move is a code change in the
  operator, not a values flip. Tracked as future work.

### Added
- **Stored-key (Shamir) unseal mode.** `VaultTransitUnseal.spec.mode` selects
  between `transit` (default) and `stored-key`; the latter submits the key
  share(s) from an in-cluster Secret to `sys/unseal` for a root-of-trust Vault
  that has no transit provider. `spec.storedKey.secretRef` defaults to
  `vault-unseal-keys` / `unseal-keys.txt` in the Vault namespace. CR example in
  `config/samples/vault-stored-key.yaml`.
- `vaultPod.scheme` (`http` | `https`) for the addresses the operator builds
  itself. Unset keeps the previous hard-coded `http`.
- Pod watch, so a Vault that seals reaches the operator at once instead of
  waiting out `monitoring.checkInterval`.
- Metric `vault_operator_unseal_attempts_total{mode,result}`; status fields
  `unsealMode` and `lastUnsealTime`; condition `KeySecretPresent`.

### Changed
- `spec.transitVault` is no longer a required property, so a stored-key
  resource can omit it. Transit resources are unaffected — an absent or empty
  `mode` resolves to `transit`.

### Notes
- No RBAC change was needed for stored-key mode: the ClusterRole already reads
  Secrets cluster-wide. The operator only ever reads the unseal Secret.

- Initial Helm chart implementation
- Support for all operator configuration options
- CRD installation as part of the chart
- Optional ServiceMonitor for Prometheus integration
- Optional PodDisruptionBudget for high availability
- Optional NetworkPolicy for security
- Configurable resource limits and requests
- Security contexts with sensible defaults
- Support for custom annotations and labels
- Comprehensive documentation
- GitHub Actions workflows for automated testing and publishing
- Chart signing with Cosign (optional)
- Automated dependency updates

### Security
- Default security contexts with non-root user
- Read-only root filesystem
- Dropped all capabilities
- Network policies to restrict traffic

## Chart Configuration Highlights

### Default Security Settings
```yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532
  fsGroup: 65532

securityContext:
  allowPrivilegeEscalation: false
  capabilities:
    drop:
    - ALL
  readOnlyRootFilesystem: true
```

### Monitoring
- ServiceMonitor CRD for Prometheus Operator
- Metrics exposed via kube-rbac-proxy for security

### High Availability
- Support for multiple replicas
- PodDisruptionBudget configuration
- Anti-affinity rules (configurable)

---

For operator application changes, see the main project [releases](https://github.com/fredericrous/vault-transit-unseal-operator/releases).