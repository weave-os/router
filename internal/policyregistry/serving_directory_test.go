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
	binding, err := json.Marshal(localRuntimeBinding(digest, "sha256:"+strings.Repeat("b", 64)))
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

	otherGeneration := ref
	otherGeneration.Generation = 2
	_, _, err = store.ReadServingObject(context.Background(), ServingClassifierReleases, otherGeneration)
	require.ErrorContains(t, err, "generation")
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

func TestDirectoryServingStoreRejectsMalformedRuntimeBinding(t *testing.T) {
	payload, err := CanonicalBytes(classifierContractRelease())
	require.NoError(t, err)
	digest := Digest(payload)
	binding, err := json.Marshal(localRuntimeBinding(digest, "not-a-digest"))
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, WriteDirectoryServingRegistry(dir, classifierContractRegistryRoot, map[string][]byte{
		servingArtifactsNamespace + digest + ".json":                payload,
		DirectoryServingRuntimeBindingPath(EnvironmentProd, digest): binding,
	}))
	store, err := NewDirectoryServingStore(dir)
	require.NoError(t, err)
	_, err = store.ReadClassifierRuntimeBinding(context.Background(), EnvironmentProd, digest)
	require.ErrorContains(t, err, "image or configuration digest is invalid")
}

func localRuntimeBinding(releaseSHA256, imageDigest string) ClassifierRuntimeBinding {
	return ClassifierRuntimeBinding{
		SchemaVersion: ClassifierRuntimeBindingSchemaV1, Environment: EnvironmentProd, ProjectID: "local", ReleaseSHA256: releaseSHA256,
		WorkerRevision: "local", WorkerURL: "http://localhost:8093", WorkerAudience: "http://localhost:8093",
		ImageDigest: imageDigest, ConfigurationSHA256: strings.Repeat("c", 64),
	}
}
