package policyregistry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectoryServingStoreReadsContentAddressedObjectsUnderItsRoot(t *testing.T) {
	release := classifierContractRelease()
	payload, err := CanonicalBytes(release)
	require.NoError(t, err)
	digest := Digest(payload)
	binding, err := json.Marshal(ClassifierRuntimeBinding{SchemaVersion: ClassifierRuntimeBindingSchemaV1, Environment: EnvironmentProd, ReleaseSHA256: digest, WorkerURL: "http://localhost:8093"})
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, WriteDirectoryServingRegistry(dir, classifierContractRegistryRoot, map[string][]byte{
		servingArtifactsNamespace + digest + ".json":                payload,
		DirectoryServingRuntimeBindingPath(EnvironmentProd, digest): binding,
	}))

	store, err := NewDirectoryServingStore(dir)
	require.NoError(t, err)
	require.Equal(t, classifierContractRegistryRoot, store.RootURI())
	ref := ObjectRef{URI: classifierContractRegistryRoot + "/" + servingArtifactsNamespace + digest + ".json", SHA256: digest, Generation: 1}
	manifest, read, err := store.ReadServingObject(context.Background(), ServingClassifierReleases, ref)
	require.NoError(t, err)
	require.Equal(t, &release, manifest)
	require.Equal(t, payload, read)

	resolved, err := store.ReadClassifierRuntimeBinding(context.Background(), EnvironmentProd, digest)
	require.NoError(t, err)
	require.Equal(t, "http://localhost:8093", resolved.Binding.WorkerURL)
	require.Equal(t, ClassifierRuntimeBindingURI(classifierContractRegistryRoot, EnvironmentProd, digest), resolved.Ref.URI)
	_, err = store.ReadClassifierRuntimeBinding(context.Background(), EnvironmentStaging, digest)
	require.ErrorIs(t, err, ErrNotFound)

	tampered := ref
	tampered.SHA256 = strings.Repeat("0", 64)
	_, _, err = store.ReadServingObject(context.Background(), ServingClassifierReleases, tampered)
	require.Error(t, err)
	_, err = store.ReadServingState(context.Background(), TargetStable)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.CompareAndSwapServingState(context.Background(), ServingControlState{}, 0)
	require.ErrorContains(t, err, "read-only")
}

func TestDirectoryServingStoreRequiresAGCSRoot(t *testing.T) {
	dir := t.TempDir()
	require.ErrorContains(t, WriteDirectoryServingRegistry(dir, "file:///registry", map[string][]byte{}), "gs:// URI")
	_, err := NewDirectoryServingStore(dir)
	require.ErrorIs(t, err, ErrNotFound)
}
