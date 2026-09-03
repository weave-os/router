// Command check_docs validates repository-local Markdown links and qualified
// references to exported Go symbols in prose.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	inlineLinkPattern = regexp.MustCompile(`!?\[[^\]]*\]\(\s*(?:<([^>]+)>|([^\s)]+))(?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))?\s*\)`)
	referencePattern  = regexp.MustCompile(`^\s*\[[^]]+\]:\s*(?:<([^>]+)>|(\S+))`)
	inlineCodePattern = regexp.MustCompile("`([^`\\n]+)`")
	symbolPattern     = regexp.MustCompile(`^([a-z][a-z0-9_]*)\.([A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)*)$`)
)

type exceptionKey struct{ kind, document, reference string }
type exceptionSet map[exceptionKey]bool
type finding struct {
	document string
	line     int
	message  string
}

func main() {
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	findings, err := checkRepository(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if len(findings) > 0 {
		for _, problem := range findings {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", problem.document, problem.line, problem.message)
		}
		os.Exit(1)
	}
	fmt.Println("Markdown links and Go symbol references passed")
}

func repositoryRoot() (string, error) {
	output, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find repository root: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func checkRepository(root string) ([]finding, error) {
	markdownFiles, err := repositoryFiles(root, "*.md")
	if err != nil {
		return nil, err
	}
	goFiles, err := repositoryFiles(root, "*.go")
	if err != nil {
		return nil, err
	}
	exceptions, err := loadExceptions(filepath.Join(root, "scripts", "docs_check_exceptions.txt"))
	if err != nil {
		return nil, err
	}
	packages, err := collectPackageSymbols(root, goFiles)
	if err != nil {
		return nil, err
	}

	var findings []finding
	for _, document := range markdownFiles {
		if filepath.Base(document) == "AGENTS.md" {
			continue // Generated mirrors are checked byte-for-byte separately.
		}
		problems, err := checkMarkdownFile(root, document, packages, exceptions)
		if err != nil {
			return nil, err
		}
		findings = append(findings, problems...)
	}
	for exception, used := range exceptions {
		if !used {
			findings = append(findings, finding{document: exception.document, message: fmt.Sprintf("unused %s exception for %s", exception.kind, exception.reference)})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].document != findings[j].document {
			return findings[i].document < findings[j].document
		}
		if findings[i].line != findings[j].line {
			return findings[i].line < findings[j].line
		}
		return findings[i].message < findings[j].message
	})
	return findings, nil
}

func repositoryFiles(root, pattern string) ([]string, error) {
	command := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", pattern)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list repository %s files: %w", pattern, err)
	}
	trimmed := strings.TrimSuffix(string(output), "\x00")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\x00"), nil
}

func loadExceptions(path string) (exceptionSet, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open documentation exceptions: %w", err)
	}
	defer file.Close()
	exceptions := exceptionSet{}
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || (fields[0] != "link" && fields[0] != "symbol") {
			return nil, fmt.Errorf("%s:%d: expected '<link|symbol> <Markdown file> <reference>'", path, lineNumber)
		}
		key := exceptionKey{fields[0], filepath.ToSlash(fields[1]), fields[2]}
		if _, exists := exceptions[key]; exists {
			return nil, fmt.Errorf("%s:%d: duplicate exception", path, lineNumber)
		}
		exceptions[key] = false
	}
	return exceptions, scanner.Err()
}

func collectPackageSymbols(root string, goFiles []string) (map[string]map[string]struct{}, error) {
	packages := map[string]map[string]struct{}{}
	files := token.NewFileSet()
	for _, relativePath := range goFiles {
		if strings.HasSuffix(relativePath, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, filepath.Join(root, relativePath), nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", relativePath, err)
		}
		symbols := packages[parsed.Name.Name]
		if symbols == nil {
			symbols = map[string]struct{}{}
			packages[parsed.Name.Name] = symbols
		}
		for _, declaration := range parsed.Decls {
			collectDeclarationSymbols(symbols, declaration)
		}
	}
	return packages, nil
}

func collectDeclarationSymbols(symbols map[string]struct{}, declaration ast.Decl) {
	switch declaration := declaration.(type) {
	case *ast.FuncDecl:
		if ast.IsExported(declaration.Name.Name) {
			symbols[declaration.Name.Name] = struct{}{}
		}
	case *ast.GenDecl:
		for _, specification := range declaration.Specs {
			switch specification := specification.(type) {
			case *ast.TypeSpec:
				if ast.IsExported(specification.Name.Name) {
					symbols[specification.Name.Name] = struct{}{}
				}
				collectFieldSymbols(symbols, specification.Type)
			case *ast.ValueSpec:
				for _, name := range specification.Names {
					if ast.IsExported(name.Name) {
						symbols[name.Name] = struct{}{}
					}
				}
			}
		}
	}
}

func collectFieldSymbols(symbols map[string]struct{}, expression ast.Expr) {
	var fields *ast.FieldList
	switch expression := expression.(type) {
	case *ast.StructType:
		fields = expression.Fields
	case *ast.InterfaceType:
		fields = expression.Methods
	default:
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			if ast.IsExported(name.Name) {
				symbols[name.Name] = struct{}{}
			}
		}
	}
}

func checkMarkdownFile(root, document string, packages map[string]map[string]struct{}, exceptions exceptionSet) ([]finding, error) {
	file, err := os.Open(filepath.Join(root, filepath.FromSlash(document)))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", document, err)
	}
	defer file.Close()
	var findings []finding
	inFence := false
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, target := range markdownTargets(line) {
			if problem := checkRelativeTarget(root, document, target, exceptions); problem != "" {
				findings = append(findings, finding{document, lineNumber, problem})
			}
		}
		for _, match := range inlineCodePattern.FindAllStringSubmatch(line, -1) {
			if problem := checkSymbolReference(document, match[1], packages, exceptions); problem != "" {
				findings = append(findings, finding{document, lineNumber, problem})
			}
		}
	}
	return findings, scanner.Err()
}

func markdownTargets(line string) []string {
	var targets []string
	for _, match := range inlineLinkPattern.FindAllStringSubmatch(line, -1) {
		targets = append(targets, firstNonempty(match[1], match[2]))
	}
	if match := referencePattern.FindStringSubmatch(line); match != nil && !strings.HasPrefix(strings.TrimSpace(line), "[^") {
		targets = append(targets, firstNonempty(match[1], match[2]))
	}
	return targets
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func checkRelativeTarget(root, document, target string, exceptions exceptionSet) string {
	if target == "" || strings.HasPrefix(target, "#") {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return fmt.Sprintf("invalid link target %q: %v", target, err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || strings.HasPrefix(parsed.Path, "/") {
		return ""
	}
	decodedPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return fmt.Sprintf("invalid escaped link target %q: %v", target, err)
	}
	if decodedPath == "" {
		return ""
	}
	resolved := filepath.Join(root, filepath.Dir(filepath.FromSlash(document)), filepath.FromSlash(decodedPath))
	_, err = os.Stat(resolved)
	if err == nil {
		if hasException(exceptions, "link", document, target) && isIgnoredPath(root, resolved) {
			useException(exceptions, "link", document, target)
		}
		return ""
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("cannot inspect link target %q: %v", target, err)
	}
	if useException(exceptions, "link", document, target) {
		return ""
	}
	return fmt.Sprintf("relative link target does not exist: %s", target)
}

func isIgnoredPath(root, path string) bool {
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return exec.Command("git", "-C", root, "check-ignore", "--quiet", "--", relativePath).Run() == nil
}

func checkSymbolReference(document, reference string, packages map[string]map[string]struct{}, exceptions exceptionSet) string {
	match := symbolPattern.FindStringSubmatch(reference)
	if match == nil {
		useException(exceptions, "symbol", document, reference)
		return ""
	}
	symbols, internalPackage := packages[match[1]]
	if !internalPackage {
		return ""
	}
	for _, symbol := range strings.Split(match[2], ".") {
		if _, exists := symbols[symbol]; exists {
			continue
		}
		if useException(exceptions, "symbol", document, reference) {
			return ""
		}
		return fmt.Sprintf("qualified Go symbol does not exist: %s", reference)
	}
	return ""
}

func useException(exceptions exceptionSet, kind, document, reference string) bool {
	key := exceptionKey{kind, filepath.ToSlash(document), reference}
	if !hasException(exceptions, kind, document, reference) {
		return false
	}
	exceptions[key] = true
	return true
}

func hasException(exceptions exceptionSet, kind, document, reference string) bool {
	_, exists := exceptions[exceptionKey{kind, filepath.ToSlash(document), reference}]
	return exists
}
