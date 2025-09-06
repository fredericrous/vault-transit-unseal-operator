# GitHub Actions Configuration

## Required Secrets

### `GHCR_TOKEN`
A GitHub Personal Access Token (PAT) with `write:packages` scope is required to push images to GitHub Container Registry.

To create the token:
1. Go to GitHub Settings > Developer settings > Personal access tokens > Tokens (classic)
2. Click "Generate new token (classic)"
3. Give it a descriptive name like "vault-operator-ghcr"
4. Select the `write:packages` scope (this will auto-select `read:packages` too)
5. Set an expiration date
6. Generate the token and copy it

To add it to the repository:
1. Go to your repository settings
2. Navigate to Secrets and variables > Actions
3. Click "New repository secret"
4. Name: `GHCR_TOKEN`
5. Value: Paste your PAT
6. Click "Add secret"

## Workflow Overview

### ci.yml
This workflow handles both CI (Continuous Integration) and CD (Continuous Deployment):

#### For Pull Requests:
- Runs tests
- Checks code formatting
- Builds Docker image (not pushed)
- Comments on PR with build status

#### For Main Branch (push/merge):
- Runs tests
- Checks code formatting
- Builds Docker image
- Increments version (patch by default)
- Pushes image with version tag and `latest`
- Creates GitHub release with release notes

### Version Incrementing
The workflow automatically increments the patch version. If you need to bump minor or major versions, manually create a tag:

```bash
git tag v0.2.0
git push origin v0.2.0
```

The next automated build will increment from there (v0.2.0 → v0.2.1).