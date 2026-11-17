package catalog

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var familyVersionPattern = regexp.MustCompile(`^(.+?)(\d+)(?:[.-](\d+))?(-[a-z][a-z0-9\-]*)?$`)

func FamilyAndVersion(id string) (family string, version [2]int, ok bool) {
	m := familyVersionPattern.FindStringSubmatch(id)
	if m == nil {
		return "", [2]int{}, false
	}
	major, err := strconv.Atoi(m[2])
	if err != nil {
		return "", [2]int{}, false
	}
	minor := 0
	if m[3] != "" {
		minor, err = strconv.Atoi(m[3])
		if err != nil {
			return "", [2]int{}, false
		}
	}
	return strings.TrimSuffix(m[1], "-") + m[4], [2]int{major, minor}, true
}

func versionLess(a, b [2]int) bool {
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	return a[1] < b[1]
}

func LatestInFamily(id string, eligible func(string) bool) string {
	model, known := ByID(id)
	if !known {
		return ""
	}
	id = model.ID
	family, version, versioned := FamilyAndVersion(id)
	latest := ""
	if eligible(id) {
		latest = id
	}
	if !versioned {
		return latest
	}
	for _, candidate := range Models {
		candidateFamily, candidateVersion, ok := FamilyAndVersion(candidate.ID)
		if !ok || candidate.ForceModelOnly || candidateFamily != family || !versionLess(version, candidateVersion) || !eligible(candidate.ID) {
			continue
		}
		latest, version = candidate.ID, candidateVersion
	}
	return latest
}

func FamilyDuplicates(ids []string) []Duplicate {
	type member struct {
		id      string
		version [2]int
	}
	byFamily := make(map[string][]member)
	for _, id := range ids {
		family, version, ok := FamilyAndVersion(id)
		if !ok {
			continue
		}
		byFamily[family] = append(byFamily[family], member{id: id, version: version})
	}

	var out []Duplicate
	for family, members := range byFamily {
		if len(members) < 2 {
			continue
		}
		newest := members[0]
		for _, m := range members[1:] {
			if versionLess(newest.version, m.version) {
				newest = m
			}
		}
		for _, m := range members {
			if m.id == newest.id {
				continue
			}
			out = append(out, Duplicate{Family: family, Superseded: m.id, SupersededBy: newest.id})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Superseded < out[j].Superseded
	})
	return out
}

type Duplicate struct {
	Family       string
	Superseded   string
	SupersededBy string
}

func (d Duplicate) String() string {
	return fmt.Sprintf("family %q: %q is superseded by %q (already deployed) — drop it from deployed_models", d.Family, d.Superseded, d.SupersededBy)
}
