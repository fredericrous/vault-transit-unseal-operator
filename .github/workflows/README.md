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

### build-and-push.yml
This workflow:
- Runs on every push to main and on pull requests
- Runs tests for all commits
- Only builds and pushes images for commits to main
- Automatically increments the patch version (e.g., v0.1.4 → v0.1.5)
- Pushes images with both version tag and `latest` tag
- Creates a GitHub release with auto-generated release notes

### Version Incrementing
The workflow automatically increments the patch version. If you need to bump minor or major versions, manually create a tag:

```bash
git tag v0.2.0
git push origin v0.2.0
```

The next automated build will increment from there (v0.2.0 → v0.2.1).