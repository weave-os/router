package architecture_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	transactionStart  = regexp.MustCompile(`(?mi)^\s*BEGIN\s*;`)
	transactionCommit = regexp.MustCompile(`(?mi)^\s*COMMIT\s*;`)
	ifExistsGuard     = regexp.MustCompile(`(?i)\bIF\s+EXISTS\b`)
	numberedParameter = regexp.MustCompile(`\$[0-9]+`)
)

func TestMigrationsAreTransactional(t *testing.T) {
	findings := sqlFindings(t, filepath.Join(repositoryRoot(t), "db", "migrations"), "*.sql", untransactionalMigration)
	assert.Empty(t, findings, "wrap every migration in BEGIN; ... COMMIT; (create migrations with make migrate-create NAME=<name>)")
}

func TestDownMigrationsArePreciseRollbacks(t *testing.T) {
	findings := sqlFindings(t, filepath.Join(repositoryRoot(t), "db", "migrations"), "*.down.sql", guardedRollback)
	assert.Empty(t, findings, "down migrations undo exactly what the up migration created: drop the IF EXISTS guard")
}

func TestQueriesUseNamedParameters(t *testing.T) {
	findings := sqlFindings(t, filepath.Join(repositoryRoot(t), "db", "queries"), "*.sql", numberedParameters)
	assert.Empty(t, findings, "SQLC queries use named, cast parameters (@param::type), never numbered $1 placeholders")
}

func TestSQLConventionChecksRejectNegativeFixtures(t *testing.T) {
	assert.True(t, untransactionalMigration("ALTER TABLE router.t ADD COLUMN c TEXT;\n"))
	assert.True(t, untransactionalMigration("BEGIN;\nALTER TABLE router.t ADD COLUMN c TEXT;\n"))
	assert.False(t, untransactionalMigration("BEGIN;\nALTER TABLE router.t ADD COLUMN c TEXT;\nCOMMIT;\n"))
	assert.True(t, guardedRollback("BEGIN;\nDROP TABLE IF EXISTS router.t;\nCOMMIT;\n"))
	assert.False(t, guardedRollback("BEGIN;\nDROP TABLE router.t;\nCOMMIT;\n"))
	assert.True(t, numberedParameters("SELECT * FROM router.t WHERE id = $1;"))
	assert.False(t, numberedParameters("SELECT * FROM router.t WHERE id = @id::uuid;"))
}

func untransactionalMigration(sql string) bool {
	return !transactionStart.MatchString(sql) || !transactionCommit.MatchString(sql)
}

func guardedRollback(sql string) bool {
	return ifExistsGuard.MatchString(sql)
}

func numberedParameters(sql string) bool {
	return numberedParameter.MatchString(sql)
}

func sqlFindings(t *testing.T, directory, pattern string, violates func(string) bool) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, pattern))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no %s files in %s", pattern, directory)
	var findings []string
	for _, path := range paths {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		if violates(string(content)) {
			findings = append(findings, strings.TrimPrefix(path, directory+string(filepath.Separator)))
		}
	}
	sort.Strings(findings)
	return findings
}
