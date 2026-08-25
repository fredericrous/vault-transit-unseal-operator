package crd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The CRD ships in three places, and all three have to agree:
//
//	config/crd/bases/   the controller-gen output; the source of truth
//	pkg/crd/crds/       embedded into the binary, self-installed at startup
//	chart/.../crds/     what `helm install` applies
//
// They drift silently. A stale embedded copy means the operator installs an
// old schema over the chart's on every restart; a stale chart copy means a
// field the operator relies on is rejected at admission until someone
// restarts the pod. Neither shows up as a failing build.
//
// The filenames must match too. The charts-repo sync copies the whole chart
// directory and THEN copies config/crd/bases over it, so a chart CRD under any
// other filename is added rather than replaced, and the published chart ends up
// applying the same CRD twice from two identical files.

const (
	// repoRoot is relative to this package's directory.
	repoRoot = "../.."

	generatedCRDDir = "config/crd/bases"
	chartCRDDir     = "chart/vault-transit-unseal-operator/crds"
	embeddedCRDDir  = "crds"
)

func digest(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func readRepoFile(t *testing.T, parts ...string) []byte {
	t.Helper()

	path := filepath.Join(append([]string{repoRoot}, parts...)...)
	data, err := os.ReadFile(path) // #nosec G304 -- fixed in-repo paths, test only
	require.NoError(t, err, "reading %s", path)

	return data
}

func yamlNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(repoRoot, dir))
	require.NoError(t, err, "reading %s", dir)

	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
			names = append(names, e.Name())
		}
	}

	return names
}

// TestCRDCopiesAreIdentical fails the build the moment the three copies drift.
// Regenerating is `make manifests && make sync-crds`.
func TestCRDCopiesAreIdentical(t *testing.T) {
	generated := yamlNames(t, generatedCRDDir)
	require.NotEmpty(t, generated, "no CRDs in %s — did controller-gen run?", generatedCRDDir)

	for _, name := range generated {
		t.Run(name, func(t *testing.T) {
			// Compare digests, not contents: the fix for a mismatch is
			// always "re-copy the generated file", never "read the diff",
			// and a testify diff of a 600-line CRD buries the CI log.
			want := digest(readRepoFile(t, generatedCRDDir, name))

			// The embedded copy is read through the same embed.FS the
			// operator self-installs from, not off disk, so this asserts
			// what actually ships in the binary.
			embedded, err := crds.ReadFile(filepath.Join(embeddedCRDDir, name))
			require.NoError(t, err, "%s is not embedded in pkg/crd", name)
			assert.Equal(t, want, digest(embedded),
				"embedded CRD is stale; run `make sync-crds` (diff: %s/%s vs pkg/crd/%s/%s)",
				generatedCRDDir, name, embeddedCRDDir, name)

			assert.Equal(t, want, digest(readRepoFile(t, chartCRDDir, name)),
				"chart CRD is stale; run `make sync-crds` (diff: %s/%s vs %s/%s)",
				generatedCRDDir, name, chartCRDDir, name)
		})
	}
}

// TestChartShipsExactlyTheGeneratedCRDs guards the filenames, which is the part
// the charts-repo sync gets wrong: an extra file under a legacy name is not a
// cosmetic duplicate, it is a second copy of the CRD applied on every install.
func TestChartShipsExactlyTheGeneratedCRDs(t *testing.T) {
	generated := yamlNames(t, generatedCRDDir)
	chart := yamlNames(t, chartCRDDir)

	assert.ElementsMatch(t, generated, chart,
		"chart/crds must hold exactly the controller-gen filenames — an extra file is a duplicate CRD, a missing one is an uninstallable chart")
}

// TestEmbeddedCRDsAreNotDuplicated keeps the same promise for the binary.
func TestEmbeddedCRDsAreNotDuplicated(t *testing.T) {
	entries, err := crds.ReadDir(embeddedCRDDir)
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}

	assert.ElementsMatch(t, yamlNames(t, generatedCRDDir), names)
}
