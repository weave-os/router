package architecture_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"
)

var (
	dependencyInjectionModules = []string{
		"go.uber.org/dig", "go.uber.org/fx", "github.com/google/wire",
		"github.com/samber/do", "github.com/goava/di", "github.com/sarulabs/di", "github.com/facebookgo/inject",
	}
	trainedArtifactExtensions = []string{".pkl", ".pickle", ".joblib", ".safetensors", ".onnx", ".pt", ".pth", ".ckpt"}
	foreignKeyTarget          = regexp.MustCompile(`(?i)\bREFERENCES\s+("?[A-Za-z_][\w."]*)`)
)

func TestGoModRequiresNoDependencyInjectionFramework(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "go.mod"))
	require.NoError(t, err)
	assert.Empty(t, requiredDependencyInjectionModules(t, string(content)), "compose dependencies with plain constructor calls in cmd/router/main.go; drop the DI container from go.mod")
}

func TestMigrationForeignKeysStayInRouterSchema(t *testing.T) {
	directory := filepath.Join(repositoryRoot(t), "db", "migrations")
	paths, err := filepath.Glob(filepath.Join(directory, "*.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	var findings []string
	for _, path := range paths {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, target := range foreignKeysOutsideRouterSchema(string(content)) {
			findings = append(findings, filepath.Base(path)+": "+target)
		}
	}
	assert.Empty(t, findings, "foreign keys may only reference router.* tables: keep external ids such as organization_id and created_by as plain opaque columns")
}

func TestNoTrainedArtifactsAreTracked(t *testing.T) {
	output, err := exec.CommandContext(t.Context(), "git", "-C", repositoryRoot(t), "ls-files", "-z").Output()
	require.NoError(t, err)
	assert.Empty(t, trainedArtifacts(nulSeparatedPaths(output)), "trained model artifacts are never committed: git rm them and use synthetic fixtures in tests, or skip when the artifact package is absent")
}

func TestGuideRuleChecksRejectNegativeFixtures(t *testing.T) {
	assert.Equal(t, []string{"go.uber.org/fx", "github.com/google/wire"}, requiredDependencyInjectionModules(t, "module m\n\nrequire (\n\tgo.uber.org/fx v1.0.0\n\tgithub.com/google/uuid v1.6.0\n)\nrequire\tgithub.com/google/wire v0.6.0 // indirect\n"))
	assert.Empty(t, requiredDependencyInjectionModules(t, "module m\n\nrequire github.com/google/wireguard v1.0.0\n\nexclude go.uber.org/fx v1.0.0\n"))
	assert.Equal(t, []string{"public.users", "orgs"}, foreignKeysOutsideRouterSchema("BEGIN;\nCREATE TABLE router.t (u UUID REFERENCES public.users(id), o UUID REFERENCES orgs (id), i UUID REFERENCES router.model_router_installations(id));\nCOMMIT;\n"))
	assert.Empty(t, foreignKeysOutsideRouterSchema("-- REFERENCES public.users\nCOMMENT ON COLUMN router.t.u IS 'REFERENCES public.users';\nCOMMENT ON TABLE router.t IS $$REFERENCES public.users$$;\nCOMMENT ON TABLE router.t IS $note$REFERENCES orgs$note$;\n"))
	assert.Equal(t, []string{"public.users"}, foreignKeysOutsideRouterSchema("COMMENT ON TABLE router.t IS $$x$$;\nALTER TABLE router.t ADD FOREIGN KEY (u) REFERENCES public.users(id);\n"))
	assert.Equal(t, []string{"router/model.joblib", "eval/weights.PT", "模型/weights.pt"}, trainedArtifacts(nulSeparatedPaths([]byte("router/model.joblib\x00eval/weights.PT\x00模型/weights.pt\x00router/model.go\x00docs/pickle.md\x00"))))
}

func requiredDependencyInjectionModules(t *testing.T, goMod string) []string {
	t.Helper()
	parsed, err := modfile.ParseLax("go.mod", []byte(goMod), nil)
	require.NoError(t, err)
	var found []string
	for _, requirement := range parsed.Require {
		for _, module := range dependencyInjectionModules {
			if requirement.Mod.Path == module || strings.HasPrefix(requirement.Mod.Path, module+"/") {
				found = append(found, requirement.Mod.Path)
			}
		}
	}
	return found
}

func nulSeparatedPaths(output []byte) []string {
	return strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
}

func foreignKeysOutsideRouterSchema(sql string) []string {
	var targets []string
	for _, match := range foreignKeyTarget.FindAllStringSubmatch(sqlCode(sql), -1) {
		target := strings.ReplaceAll(match[1], `"`, "")
		if !strings.HasPrefix(strings.ToLower(target), "router.") {
			targets = append(targets, target)
		}
	}
	return targets
}

func trainedArtifacts(paths []string) []string {
	var found []string
	for _, path := range paths {
		extension := strings.ToLower(filepath.Ext(path))
		for _, artifact := range trainedArtifactExtensions {
			if extension == artifact {
				found = append(found, path)
			}
		}
	}
	return found
}
