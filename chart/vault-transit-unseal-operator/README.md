# vault-transit-unseal-operator

A Kubernetes operator that automatically manages HashiCorp Vault initialization and unsealing using transit unseal.

## TL;DR

```bash
helm repo add vault-transit-unseal https://fredericrous.github.io/charts
helm repo update
helm install vault-transit-unseal-operator vault-transit-unseal/vault-transit-unseal-operator \
  --namespace vault-transit-unseal-system \
  --create-namespace
```

## Prerequisites

- Kubernetes 1.24+
- Helm 3.0+
- A transit Vault instance for unsealing

## Installing the Chart

To install the chart with the release name `vault-transit-unseal-operator`:

```bash
helm install vault-transit-unseal-operator vault-transit-unseal/vault-transit-unseal-operator \
  --namespace vault-transit-unseal-system \
  --create-namespace
```

## Uninstalling the Chart

To uninstall/delete the `vault-transit-unseal-operator` deployment:

```bash
helm delete vault-transit-unseal-operator -n vault-transit-unseal-system
```

The command removes all the Kubernetes components associated with the chart and deletes the release.

## Configuration

The following table lists the configurable parameters of the vault-transit-unseal-operator chart and their default values.

| Parameter | Description | Default |
| --------- | ----------- | ------- |
| `replicaCount` | Number of replicas | `1` |
| `image.repository` | Image repository | `ghcr.io/fredericrous/vault-transit-unseal-operator` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `nameOverride` | Override chart name | `""` |
| `fullnameOverride` | Override full name | `""` |
| `createNamespace` | Create namespace if it doesn't exist | `false` |
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `serviceAccount.name` | Service account name | `""` |
| `podAnnotations` | Pod annotations | `{}` |
| `podSecurityContext` | Pod security context | `{runAsNonRoot: true}` |
| `controllerManager.resources` | Controller manager resources | See values.yaml |
| `controllerManager.securityContext` | Controller manager security context | See values.yaml |
| `controllerManager.manager.args` | Controller manager arguments | See values.yaml |
| `kubeRbacProxy.enabled` | Enable kube-rbac-proxy for metrics | `true` |
| `kubeRbacProxy.image.repository` | kube-rbac-proxy image | `gcr.io/kubebuilder/kube-rbac-proxy` |
| `kubeRbacProxy.image.tag` | kube-rbac-proxy image tag | `v0.13.1` |
| `serviceMonitor.enabled` | Create ServiceMonitor for Prometheus | `false` |
| `podDisruptionBudget.enabled` | Create PodDisruptionBudget | `false` |
| `networkPolicy.enabled` | Create NetworkPolicy | `false` |

### Example values.yaml for production

```yaml
replicaCount: 2

image:
  tag: "v0.3.0"

podDisruptionBudget:
  enabled: true
  minAvailable: 1

networkPolicy:
  enabled: true

serviceMonitor:
  enabled: true
  labels:
    prometheus: kube-prometheus

controllerManager:
  resources:
    limits:
      cpu: 1000m
      memory: 512Mi
    requests:
      cpu: 100m
      memory: 128Mi
```

## Post-Installation

After installing the operator, you need to:

1. Create a transit token secret:
```bash
kubectl create secret generic vault-transit-token \
  -n vault \
  --from-literal=token=<YOUR_TRANSIT_TOKEN>
```

2. Create a VaultTransitUnseal resource:
```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-main
  namespace: vault
spec:
  vaultPod:
    namespace: vault
    selector:
      app: vault
  transitVault:
    address: http://your-transit-vault:8200
    secretRef:
      name: vault-transit-token
  postUnsealConfig:
    enableKV: true
```

## Monitoring

If you have Prometheus installed, enable metrics collection:

```bash
helm upgrade vault-transit-unseal-operator vault-transit-unseal/vault-transit-unseal-operator \
  --set serviceMonitor.enabled=true \
  --set serviceMonitor.labels.prometheus=kube-prometheus
```

## Security Considerations

- The operator requires cluster-wide permissions to watch and manage Vault pods
- Transit tokens should be rotated regularly
- Consider using network policies to restrict traffic
- Use RBAC to limit access to VaultTransitUnseal resources

## Troubleshooting

### View operator logs
```bash
kubectl logs -n vault-transit-unseal-system deployment/vault-transit-unseal-operator -f
```

### Check operator status
```bash
kubectl get deployment -n vault-transit-unseal-system
kubectl get pods -n vault-transit-unseal-system
```

### Verify CRDs are installed
```bash
kubectl get crd vaulttransitunseals.vault.homelab.io
```