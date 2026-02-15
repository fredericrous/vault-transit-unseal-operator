# Changelog

All notable changes to the Vault Transit Unseal Operator Helm chart will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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