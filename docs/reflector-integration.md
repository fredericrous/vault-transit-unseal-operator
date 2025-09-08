# Kubernetes Reflector Integration

The vault-transit-unseal-operator now supports adding custom annotations to secrets it creates, enabling seamless integration with [Kubernetes Reflector](https://github.com/emberstack/kubernetes-reflector).

## Configuration

You can specify annotations for the admin token and recovery keys secrets through the CRD:

```yaml
apiVersion: vault.k8start.dev/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault
  namespace: vault
spec:
  initialization:
    secretNames:
      adminToken: vault-admin-token
      recoveryKeys: vault-keys
      
      # Reflector annotations for admin token
      adminTokenAnnotations:
        reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
        reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "external-secrets,postgres,haproxy-controller"
        reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"
      
      # Optional: annotations for recovery keys
      recoveryKeysAnnotations:
        app.kubernetes.io/managed-by: "vault-transit-unseal-operator"
```

## Benefits

1. **No Manual Patching**: Annotations are added automatically when the secrets are created
2. **GitOps Friendly**: Everything is declarative in the CRD
3. **Flexible**: Add any annotations you need, not just Reflector ones
4. **Backward Compatible**: If no annotations are specified, behavior is unchanged

## Use Cases

### Cross-Namespace Secret Sharing
The most common use case is sharing the `vault-admin-token` across namespaces that need to interact with Vault:

```yaml
adminTokenAnnotations:
  reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
  reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "external-secrets,argocd,monitoring"
  reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"
```

### Custom Metadata
You can also add organizational metadata:

```yaml
adminTokenAnnotations:
  company.com/owner: "platform-team"
  company.com/classification: "sensitive"
  company.com/rotation-policy: "90d"
```

## Implementation Details

The operator uses a new `CreateOrUpdateWithOptions` method that accepts an annotations map. This method:
- Preserves existing annotations on the secret
- Merges new annotations from the CRD
- Is backward compatible with the existing `CreateOrUpdate` method