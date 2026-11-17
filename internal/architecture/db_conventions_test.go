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
	transactionStart  = regexp.MustCompile(`(?i)^BEGIN\s*;`)
	transactionCommit = regexp.MustCompile(`(?i)\bCOMMIT\s*;$`)
	ifExistsGuard     = regexp.MustCompile(`(?i)\bIF\s+EXISTS\b`)
	numberedParameter = regexp.MustCompile(`\$[0-9]+`)
	queryName         = regexp.MustCompile(`(?m)^-- name: (\w+) :`)
	dollarQuoteTag    = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)
	queryNamePrefix   = regexp.MustCompile(`^(` + strings.Join(queryNameVerbs, "|") + `)[A-Z]`)
)

var queryNameVerbs = []string{
	"Insert", "Upsert", "Get", "Update", "Delete",
	"Acquire", "Add", "Advance", "Check", "Claim", "Cooldown", "Count", "Create", "Debit", "Disable",
	"Expire", "Extend", "Finalize", "Has", "Increment", "List", "Lock", "Mark", "Persist", "Record",
	"Refresh", "Release", "Reserve", "Reset", "SoftDelete", "Sweep", "TryAcquire",
}

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

func TestQueriesUsePrefixedNames(t *testing.T) {
	directory := filepath.Join(repositoryRoot(t), "db", "queries")
	paths, err := filepath.Glob(filepath.Join(directory, "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	var findings []string
	for _, path := range paths {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, name := range unprefixedQueryNames(string(content)) {
			findings = append(findings, filepath.Base(path)+": "+name)
		}
	}
	assert.Empty(t, findings, "start the query name with one of: %s", strings.Join(queryNameVerbs, ", "))
}

func TestSQLConventionChecksRejectNegativeFixtures(t *testing.T) {
	assert.True(t, untransactionalMigration("ALTER TABLE router.t ADD COLUMN c TEXT;\n"))
	assert.True(t, untransactionalMigration("BEGIN;\nALTER TABLE router.t ADD COLUMN c TEXT;\n"))
	assert.True(t, untransactionalMigration("ALTER TABLE router.t ADD COLUMN c TEXT;\nBEGIN;\nCOMMIT;\n"))
	assert.True(t, untransactionalMigration("BEGIN;\nCOMMIT;\nALTER TABLE router.t ADD COLUMN c TEXT;\n"))
	assert.False(t, untransactionalMigration("-- add c\nBEGIN;\nALTER TABLE router.t ADD COLUMN c TEXT;\nCOMMIT;\n-- done\n"))
	assert.True(t, guardedRollback("BEGIN;\nDROP TABLE IF EXISTS router.t;\nCOMMIT;\n"))
	assert.False(t, guardedRollback("BEGIN;\nDROP TABLE router.t;\nCOMMIT;\n"))
	assert.False(t, guardedRollback("-- Do not use IF EXISTS\nBEGIN;\n/* IF EXISTS */ COMMENT ON TABLE router.t IS 'IF EXISTS';\nCOMMIT;\n"))
	assert.True(t, numberedParameters("SELECT * FROM router.t WHERE id = $1;"))
	assert.False(t, numberedParameters("SELECT * FROM router.t WHERE id = @id::uuid;"))
	assert.Equal(t, []string{"FetchThing", "Getter"}, unprefixedQueryNames("-- name: FetchThing :one\n-- name: Getter :one\n-- name: GetThing :one\n-- name: SoftDeleteThing :exec\n"))
	assert.False(t, numberedParameters("-- never $1\nSELECT * FROM router.t WHERE note = 'costs $1' AND id = @id::uuid;"))
}

func untransactionalMigration(sql string) bool {
	code := strings.TrimSpace(sqlCode(sql))
	return !transactionStart.MatchString(code) || !transactionCommit.MatchString(code)
}

func guardedRollback(sql string) bool {
	return ifExistsGuard.MatchString(sqlCode(sql))
}

func numberedParameters(sql string) bool {
	return numberedParameter.MatchString(sqlCode(sql))
}

func unprefixedQueryNames(sql string) []string {
	var names []string
	for _, match := range queryName.FindAllStringSubmatch(sql, -1) {
		if !queryNamePrefix.MatchString(match[1]) {
			names = append(names, match[1])
		}
	}
	return names
}

// sqlCode blanks comments and single-quoted literals so the checks only see SQL statements.
func sqlCode(sql string) string {
	var code strings.Builder
	for i := 0; i < len(sql); i++ {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			i += skipUntil(sql[i:], "\n") - 1
		case strings.HasPrefix(sql[i:], "/*"):
			i += skipUntil(sql[i+2:], "*/") + 1
		case sql[i] == '\'':
			i += skipUntil(sql[i+1:], "'")
		case dollarQuoteTag.MatchString(sql[i:]):
			tag := dollarQuoteTag.FindString(sql[i:])
			i += len(tag) - 1 + skipUntil(sql[i+len(tag):], tag)
		default:
			code.WriteByte(sql[i])
			continue
		}
		code.WriteByte(' ')
	}
	return code.String()
}

func skipUntil(rest, terminator string) int {
	if end := strings.Index(rest, terminator); end >= 0 {
		return end + len(terminator)
	}
	return len(rest)
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
