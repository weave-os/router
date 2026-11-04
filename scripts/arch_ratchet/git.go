package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type repo struct {
	root string
}

func (r repo) git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(exitErr.Stderr))
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func (r repo) mergeBase(base string) (string, error) {
	out, err := r.git("merge-base", base, "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// changedFiles lists measured Go files that differ between rev and the working tree,
// including untracked files, so local runs see uncommitted work.
func (r repo) changedFiles(rev string) ([]string, error) {
	tracked, err := r.git("diff", "--name-only", "--no-renames", rev, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := r.git("ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(tracked)+string(untracked), "\n") {
		if isMeasuredPath(line) {
			seen[line] = true
		}
	}
	return sortedKeys(seen), nil
}

func (r repo) listDirAt(rev, dir string) ([]string, error) {
	out, err := r.git("ls-tree", "--name-only", rev, dir+"/")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if isMeasuredPath(line) && filepath.Dir(line) == dir {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

func (r repo) listDirWorktree(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		path := dir + "/" + entry.Name()
		if !entry.IsDir() && isMeasuredPath(path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// readAt returns the contents of paths at rev; paths absent at rev are omitted.
func (r repo) readAt(rev string, paths []string) (map[string][]byte, error) {
	var request bytes.Buffer
	for _, path := range paths {
		fmt.Fprintf(&request, "%s:%s\n", rev, path)
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = r.root
	cmd.Stdin = &request
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file --batch: %w", err)
	}
	reader := bufio.NewReader(bytes.NewReader(out))
	files := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, found, err := readBatchEntry(reader)
		if err != nil {
			return nil, fmt.Errorf("read %s at %s: %w", path, short(rev), err)
		}
		if found {
			files[path] = content
		}
	}
	return files, nil
}

func readBatchEntry(reader *bufio.Reader) ([]byte, bool, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, false, err
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[1] == "missing" {
		return nil, false, nil
	}
	if len(fields) != 3 {
		return nil, false, fmt.Errorf("unexpected cat-file header %q", header)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, false, fmt.Errorf("cat-file size %q: %w", fields[2], err)
	}
	content := make([]byte, size)
	if _, err := io.ReadFull(reader, content); err != nil {
		return nil, false, err
	}
	if _, err := reader.ReadByte(); err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func (r repo) readWorktree(paths []string) (map[string][]byte, error) {
	files := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(r.root, path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		files[path] = content
	}
	return files, nil
}

func isMeasuredPath(path string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	for _, skipped := range []string{"vendor/", "internal/sqlc/", "node_modules/"} {
		if strings.HasPrefix(path, skipped) || strings.Contains(path, "/"+skipped) {
			return false
		}
	}
	return !strings.Contains(path, "testdata/")
}

func sortedKeys[V any](set map[string]V) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
