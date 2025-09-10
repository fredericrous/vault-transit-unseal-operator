# Release Automation Setup

This document explains how the automated release process works and how to set it up.

## Overview

When a new tag is pushed (e.g., `v0.6.0`), the following automated process occurs:

1. **Release Workflow** (`release.yml`):
   - Builds and tests the operator
   - Creates multi-platform Docker images
   - Pushes images to GitHub Container Registry
   - Creates a GitHub Release with release notes
   - Runs security scans on the images

2. **Post Release Tasks** (`post-release.yaml`):
   - Triggers when the release is published
   - Initiates the Helm chart update process

3. **Update Helm Chart** (`update-helm-chart.yaml`):
   - Updates the Helm chart in the `fredericrous/charts` repository
   - Updates `appVersion` in Chart.yaml
   - Bumps the chart version (patch increment)
   - Updates version references in README.md
   - Creates a PR with all changes

## Setup Requirements

### 1. Personal Access Token (PAT)

The automation requires a Personal Access Token with permissions to:
- Access the `fredericrous/charts` repository
- Create pull requests
- Trigger workflows

#### Creating the PAT:

1. Go to GitHub Settings → Developer settings → Personal access tokens → Classic tokens
2. Click "Generate new token (classic)"
3. Name: `CHARTS_REPO_TOKEN`
4. Select scopes:
   - `repo` (Full control of private repositories)
   - `workflow` (Update GitHub Action workflows)
5. Generate and copy the token

#### Adding the PAT to Repository Secrets:

1. Go to your `vault-transit-unseal-operator` repository
2. Settings → Secrets and variables → Actions
3. Click "New repository secret"
4. Name: `CHARTS_REPO_TOKEN`
5. Value: Paste your PAT
6. Click "Add secret"

### 2. Ensure Workflows Have Correct Permissions

In your repository settings:
1. Go to Settings → Actions → General
2. Under "Workflow permissions":
   - Select "Read and write permissions"
   - Check "Allow GitHub Actions to create and approve pull requests"

## Release Process

### Automatic Release (Recommended)

```bash
# Create and push a new tag
git tag v0.6.1
git push origin v0.6.1
```

This will:
1. Trigger the release workflow
2. Build and push Docker images
3. Create a GitHub release
4. Automatically trigger the Helm chart update
5. Create a PR in the charts repository

### Manual Trigger

If automation fails, you can manually trigger the chart update:

1. Go to Actions → "Update Helm Chart"
2. Click "Run workflow"
3. Enter the version (without 'v' prefix): `0.6.1`
4. Choose whether to bump chart version
5. Run workflow

## Troubleshooting

### Chart Update Not Triggering

1. **Check PAT permissions**: Ensure CHARTS_REPO_TOKEN has required permissions
2. **Check workflow logs**: Look for errors in the post-release workflow
3. **Manual trigger**: Use the workflow_dispatch option

### Workflow Fails with Permission Error

Ensure the PAT has:
- Access to both repositories
- `workflow` scope for triggering workflows
- `repo` scope for creating PRs

### Version Format Issues

- Tags should use format: `v0.6.1`
- The workflows automatically remove the 'v' prefix where needed
- Chart versions follow semantic versioning without 'v' prefix

## Testing the Automation

You can test without creating a real release:

```bash
# Test with a pre-release
git tag v0.6.1-beta.1
git push origin v0.6.1-beta.1
```

This creates a pre-release that won't affect the "latest" tag.

## Customization

### Changing Version Bump Strategy

Edit `update-helm-chart.yaml`:
```yaml
# Current: Patch bump (0.1.0 → 0.1.1)
NEW_CHART_VERSION="$MAJOR.$MINOR.$((PATCH + 1))"

# Alternative: Minor bump (0.1.0 → 0.2.0)
NEW_CHART_VERSION="$MAJOR.$((MINOR + 1)).0"
```

### Updating Additional Files

Add more sed commands in the "Update README.md" step to update other files:
```bash
# Update values.yaml
sed -i "s/tag: .*/tag: ${{ steps.version.outputs.version }}/g" "charts/vault-transit-unseal-operator/values.yaml"
```

## Security Considerations

- Never commit the PAT to the repository
- Rotate the PAT periodically
- Use the minimum required permissions
- Consider using GitHub App tokens for production environments