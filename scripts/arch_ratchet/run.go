package main

import (
	"cmp"
	"path"
	"path/filepath"
	"slices"
)

type runResult struct {
	baseRev      string
	checkedFiles int
	violations   []violation
}

func run(r repo, base string) (runResult, error) {
	baseRev, err := r.mergeBase(base)
	if err != nil {
		return runResult{}, err
	}
	changed, err := r.changedFiles(baseRev)
	if err != nil {
		return runResult{}, err
	}
	baseFiles, headFiles, err := measureBothSides(r, baseRev, changed)
	if err != nil {
		return runResult{}, err
	}
	allowed, err := loadExceptions(filepath.Join(r.root, exceptionsPath))
	if err != nil {
		return runResult{}, err
	}
	violations := compareFuncs(baseFiles, headFiles, allowed)
	violations = append(violations, compareFiles(baseFiles, headFiles, allowed)...)
	violations = append(violations, compareTotals(baseFiles, headFiles, allowed)...)
	if slices.ContainsFunc(changed, func(p string) bool { return path.Dir(p) == proxyDir }) {
		surfaceViolations, err := compareProxySurface(r, baseRev, allowed)
		if err != nil {
			return runResult{}, err
		}
		violations = append(violations, surfaceViolations...)
	}
	slices.SortFunc(violations, func(a, b violation) int {
		return cmp.Or(cmp.Compare(a.subject, b.subject), cmp.Compare(a.metric, b.metric))
	})
	return runResult{baseRev: baseRev, checkedFiles: len(changed), violations: violations}, nil
}

func measureBothSides(r repo, baseRev string, changed []string) (map[string]fileMetrics, map[string]fileMetrics, error) {
	baseSources, err := r.readAt(baseRev, changed)
	if err != nil {
		return nil, nil, err
	}
	headSources, err := r.readWorktree(changed)
	if err != nil {
		return nil, nil, err
	}
	baseFiles, err := measureFiles(baseSources)
	if err != nil {
		return nil, nil, err
	}
	headFiles, err := measureFiles(headSources)
	return baseFiles, headFiles, err
}

func compareProxySurface(r repo, baseRev string, allowed exceptionSet) ([]violation, error) {
	basePaths, err := r.listDirAt(baseRev, proxyDir)
	if err != nil {
		return nil, err
	}
	headPaths, err := r.listDirWorktree(proxyDir)
	if err != nil {
		return nil, err
	}
	baseSources, err := r.readAt(baseRev, basePaths)
	if err != nil {
		return nil, err
	}
	headSources, err := r.readWorktree(headPaths)
	if err != nil {
		return nil, err
	}
	baseSurface, err := measureSurface(baseSources)
	if err != nil {
		return nil, err
	}
	headSurface, err := measureSurface(headSources)
	if err != nil {
		return nil, err
	}
	var violations []violation
	violations = appendGrowth(violations, "proxy.Service", metricServiceFields, baseSurface.serviceFields, headSurface.serviceFields, allowed)
	violations = appendGrowth(violations, "proxy.Service", metricServiceMethods, baseSurface.serviceMethods, headSurface.serviceMethods, allowed)
	return appendGrowth(violations, proxyDir, metricContextKeys, baseSurface.contextKeys, headSurface.contextKeys, allowed), nil
}
