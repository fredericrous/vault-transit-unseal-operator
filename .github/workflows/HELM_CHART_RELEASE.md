# Helm Chart Release Automation

This document explains how the automated Helm chart updates work when releasing new operator versions.

## Overview

When you create a new release of the vault-transit-unseal-operator, the `update-helm-chart.yaml` workflow automatically:

1. Creates a PR in the charts repository
2. Updates the chart's `appVersion` to match the operator version
3. Optionally bumps the chart version (patch increment by default)
4. Includes release notes and testing instructions

## Setup Instructions

### 1. Create Personal Access Token

You need a token with write access to your charts repository:

1. Go to GitHub Settings → Developer settings → Personal access tokens → Tokens (classic)
2. Click "Generate new token"
3. Name: `Charts Repository Automation`
4. Expiration: Choose an appropriate expiration (recommend 1 year)
5. Select scopes:
   - `repo` (Full control of private repositories)
   - `workflow` (Update GitHub Action workflows)
6. Generate and copy the token

### 2. Add Token to Repository Secrets

1. Go to your operator repository: https://github.com/fredericrous/vault-transit-unseal-operator
2. Navigate to Settings → Secrets and variables → Actions
3. Click "New repository secret"
4. Name: `CHARTS_REPO_TOKEN`
5. Value: Paste your token
6. Click "Add secret"

### 3. Configure Charts Repository (Optional)

For auto-merge to work in the charts repository:

1. Go to https://github.com/fredericrous/charts
2. Settings → General → Pull Requests
3. Enable "Allow auto-merge"
4. Set up branch protection rules for `main`:
   - Require pull request reviews (optional, but disable for auto-merge)
   - Require status checks to pass
   - Include administrators (optional)

## Usage

### Automatic Trigger

When you create a new release:

```bash
# In your operator repository
git tag v0.2.0
git push origin v0.2.0
```

Then create a release on GitHub → the workflow triggers automatically.

### Manual Trigger

You can also trigger manually:

1. Go to Actions → Update Helm Chart
2. Click "Run workflow"
3. Enter version (without 'v' prefix): `0.2.0`
4. Choose whether to bump chart version
5. Run workflow

## Workflow Process

1. **Release Created**: When you publish a release (e.g., v0.2.0)
2. **Workflow Triggers**: Automatically starts the update process
3. **PR Created**: Opens a PR in the charts repository with:
   - Updated `appVersion: "0.2.0"`
   - Bumped chart version (e.g., 1.0.0 → 1.0.1)
   - Release notes from your operator release
   - Testing instructions
4. **Review**: Review the PR in the charts repository
5. **Merge**: Merge the PR
6. **Tag**: Create a new chart release tag as suggested in the PR

## Version Management Strategy

### Operator Versions
- Use semantic versioning: `v0.1.0`, `v0.2.0`, etc.
- Tag format: `v<major>.<minor>.<patch>`

### Chart Versions
- Independent from operator versions
- Start at `1.0.0` for stable charts
- Bump versions:
  - **Patch** (1.0.0 → 1.0.1): appVersion updates, bug fixes
  - **Minor** (1.0.1 → 1.1.0): New features, non-breaking changes
  - **Major** (1.1.0 → 2.0.0): Breaking changes

### Example Progression

| Operator Release | Chart Version | Change Type |
|-----------------|---------------|-------------|
| v0.1.0 | 1.0.0 | Initial release |
| v0.1.1 | 1.0.1 | Bug fix in operator |
| v0.2.0 | 1.0.2 | New operator features |
| v0.2.0 | 1.1.0 | Added new chart values |
| v0.3.0 | 2.0.0 | Breaking change in values |

## Troubleshooting

### Workflow Fails: "Resource not accessible by integration"

The token might lack necessary permissions. Ensure it has:
- `repo` scope for private repositories
- `public_repo` scope for public repositories

### PR Not Created

Check the Actions tab in your operator repository for workflow logs.

### Auto-merge Not Working

1. Verify branch protection rules allow auto-merge
2. Ensure required status checks are passing
3. Check that the token has workflow permissions

## Manual Process (If Automation Fails)

1. Clone charts repository
2. Update `charts/vault-transit-unseal-operator/Chart.yaml`:
   ```yaml
   version: 1.0.1  # Bump this
   appVersion: "0.2.0"  # Match operator version
   ```
3. Commit and push
4. Create PR
5. After merge, tag: `git tag vault-transit-unseal-operator-v1.0.1`