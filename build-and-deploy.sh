#!/bin/bash
set -e

# Configuration
IMAGE_REGISTRY="${IMAGE_REGISTRY:-ghcr.io/fredericrous}"
IMAGE_NAME="vault-operator"
IMAGE_TAG="${IMAGE_TAG:-latest}"
FULL_IMAGE="${IMAGE_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"

echo "Building Vault Transit Unseal Operator..."
echo "Image: ${FULL_IMAGE}"

# Build the operator
echo "Running tests..."
make test

echo "Building Docker image..."
make docker-build IMG="${FULL_IMAGE}"

echo ""
echo "Build complete!"
echo ""
echo "To deploy to your cluster:"
echo "  1. Push the image: docker push ${FULL_IMAGE}"
echo "  2. Deploy operator: kubectl apply -k ../manifests/core/vault-operator/"
echo "  3. Create transit token secret:"
echo "     kubectl create secret generic vault-transit-token \\"
echo "       -n vault \\"
echo "       --from-literal=token=<YOUR_TRANSIT_TOKEN>"
echo "  4. Apply VaultTransitUnseal resource:"
echo "     kubectl apply -f ../manifests/core/vault/vault-transit-unseal.yaml"
echo ""
echo "To check operator logs:"
echo "  kubectl logs -n vault-operator deployment/vault-operator -f"