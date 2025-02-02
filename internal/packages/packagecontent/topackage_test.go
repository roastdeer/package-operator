package packagecontent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	pkoapis "package-operator.run/apis"
	"package-operator.run/package-operator/internal/packages"
	"package-operator.run/package-operator/internal/packages/packagecontent"
	"package-operator.run/package-operator/internal/packages/packageimport"
)

var testScheme = runtime.NewScheme()

func init() {
	if err := pkoapis.AddToScheme(testScheme); err != nil {
		panic(err)
	}
}

func TestPackageFromFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	files, err := packageimport.Folder(ctx, "testdata")
	require.NoError(t, err)

	pkg, err := packagecontent.PackageFromFiles(ctx, testScheme, files)
	require.NoError(t, err)
	require.NotNil(t, pkg)
}

func TestTemplateSpecFromPackage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	files, err := packageimport.Folder(ctx, "testdata")
	require.NoError(t, err)

	pkg, err := packagecontent.PackageFromFiles(ctx, testScheme, files)
	require.NoError(t, err)
	require.NotNil(t, pkg)

	spec := packagecontent.TemplateSpecFromPackage(pkg)
	require.NotNil(t, spec)
}

func TestPackageManifestLoader_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fileMap packagecontent.Files
		err     string
	}{
		{
			name: "not found",
			err:  "Package validation errors:\n- PackageManifest not found:\n  searched at manifest.yaml,manifest.yml",
		},
		{
			name: "invalid YAML",
			fileMap: packagecontent.Files{
				packages.PackageManifestFile: []byte("{xxx..,akd:::"),
			},
			err: `Package validation errors:
- Invalid YAML in manifest.yaml:
  error converting YAML to JSON: yaml: line 1: did not find expected node content`,
		},
		{
			name: "invalid GVK",
			fileMap: packagecontent.Files{
				packages.PackageManifestFile: []byte("apiVersion: fruits/v1\nkind: Banana"),
			},
			err: `Package validation errors:
- PackageManifest unknown GVK in manifest.yaml:
  GroupKind must be PackageManifest.manifests.package-operator.run, is: Banana.fruits`,
		},
		{
			name: "unsupported Version",
			fileMap: packagecontent.Files{
				packages.PackageManifestFile: []byte("apiVersion: manifests.package-operator.run/v23\nkind: PackageManifest"),
			},
			err: `Package validation errors:
- PackageManifest unknown GVK in manifest.yaml:
  unknown version v23, supported versions: v1alpha1`,
		},
	}

	for i := range tests {
		test := tests[i]
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := packagecontent.PackageFromFiles(context.Background(), testScheme, test.fileMap)
			require.EqualError(t, err, test.err)
		})
	}
}
