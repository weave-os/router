package toolcheck

import (
	"regexp"
	"strings"
)

// Mode selects how much of the pipeline Check runs, so one model can be
// benchmarked with the repair layer off, at today's default, and with the
// semantic tier on. The proxy sets it per request from router.ToolCheckMode.
type Mode string

const (
	// ModeOff runs only the parse tier (the wire needs valid JSON); no
	// normalize, validate, or repair — the client sees arguments as emitted.
	ModeOff Mode = "off"
	// ModeStandard is the default: normalize + validate + lossless coercions.
	ModeStandard Mode = "standard"
	// ModeSemantic adds shape repairs that are meaning-preserving but not
	// purely type-driven: JSON-encoded strings where the schema wants an
	// array/object, and markdown autolinks in path-like string params.
	ModeSemantic Mode = "semantic"
)

// WithMode returns a validator that checks in mode m. Compiled schemas are
// shared (read-only), so the copy is cheap and cache-safe. A nil receiver
// stays nil in standard mode; other modes get a schema-less validator that
// still honours the mode.
func (v *Validator) WithMode(m Mode) *Validator {
	if m == "" {
		m = ModeStandard
	}
	if v == nil {
		if m == ModeStandard {
			return nil
		}
		return &Validator{mode: m}
	}
	if v.Mode() == m {
		return v
	}
	cp := *v
	cp.mode = m
	return &cp
}

// Mode reports the validator's mode; nil and zero-value receivers are standard.
func (v *Validator) Mode() Mode {
	if v == nil || v.mode == "" {
		return ModeStandard
	}
	return v.mode
}

// markdownAutolink matches a whole value of the form `[text](target)`.
var markdownAutolink = regexp.MustCompile(`^\[([^\]\n]+)\]\(([^)\s]+)\)$`)

// pathLikeKey reports whether a top-level parameter name suggests a filesystem
// path, the only place an autolink is unambiguously a rendering artefact.
func pathLikeKey(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "path") || strings.Contains(k, "file") || strings.Contains(k, "dir")
}

// stripMarkdownAutolink turns `[notes.md](http://notes.md)` into `notes.md`
// when the link target is the text itself, optionally behind a URL scheme.
// A real hyperlink whose text differs from its target is left alone.
func stripMarkdownAutolink(value string) (string, bool) {
	m := markdownAutolink.FindStringSubmatch(value)
	if m == nil {
		return value, false
	}
	text, target := m[1], m[2]
	if target == text {
		return text, true
	}
	for _, scheme := range []string{"http://", "https://", "file://"} {
		if strings.HasPrefix(target, scheme) && target[len(scheme):] == text {
			return text, true
		}
	}
	return value, false
}
