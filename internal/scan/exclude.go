package scan

import "strings"

// Matcher applies gitignore-like exclusion patterns to share-relative paths
// (FR-SCAN-15).
//
// Semantics: '*' matches within one path segment, '?' matches one character,
// '**' matches any number of segments, a trailing '/' restricts the pattern
// to directories, and a leading '!' negates. A pattern containing no '/' is
// matched against the basename at any depth ("@eaDir" excludes it wherever it
// appears); one that does contain a '/' is anchored at the share root, and a
// leading '/' anchors a single-segment pattern there too. When several
// patterns match the same path, the last one wins.
type Matcher struct {
	pats []pattern
}

type pattern struct {
	segs    []string
	dirOnly bool
	negate  bool
	raw     string
}

// NewMatcher compiles a list of patterns. Empty patterns and comments are
// ignored; the matcher is safe for concurrent use.
func NewMatcher(patterns []string) *Matcher {
	m := &Matcher{}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		var pat pattern
		pat.raw = raw
		if strings.HasPrefix(p, "!") {
			pat.negate = true
			p = p[1:]
		}
		if strings.HasSuffix(p, "/") {
			pat.dirOnly = true
			p = strings.TrimSuffix(p, "/")
		}
		anchored := strings.HasPrefix(p, "./") || strings.HasPrefix(p, "/")
		p = strings.TrimPrefix(p, "./")
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			continue
		}
		pat.segs = strings.Split(p, "/")
		// gitignore's defining rule: a pattern containing no separator is
		// matched against the basename at every level, not just at the share
		// root. Without this, the natural spellings ("@eaDir", "*.tmp")
		// silently match nothing below the top level.
		if !anchored && len(pat.segs) == 1 {
			pat.segs = []string{"**", pat.segs[0]}
		}
		m.pats = append(m.pats, pat)
	}
	return m
}

// Empty reports whether the matcher has no patterns.
func (m *Matcher) Empty() bool { return m == nil || len(m.pats) == 0 }

// Match reports whether a share-relative path is excluded.
func (m *Matcher) Match(relPath string, isDir bool) bool {
	if m.Empty() {
		return false
	}
	return m.MatchSegments(strings.Split(relPath, "/"), isDir)
}

// MatchSegments is Match on an already-split path. The crawl calls this once
// per directory entry, and the parent's segments are the same for all of them,
// so splitting per entry would re-do that work (and allocate the joined path
// and the segment slice) for every file on the share.
func (m *Matcher) MatchSegments(segs []string, isDir bool) bool {
	if m.Empty() {
		return false
	}
	excluded := false
	for i := range m.pats {
		p := &m.pats[i]
		if p.dirOnly && !isDir {
			continue
		}
		if matchSegments(p.segs, segs) {
			excluded = !p.negate
		}
	}
	return excluded
}

// splitRel splits a share-relative path into segments, reusing buf. The empty
// path has no segments, so a root-level entry matches on its name alone.
func splitRel(buf []string, relPath string) []string {
	buf = buf[:0]
	if relPath == "" {
		return buf
	}
	for {
		i := strings.IndexByte(relPath, '/')
		if i < 0 {
			return append(buf, relPath)
		}
		buf = append(buf, relPath[:i])
		relPath = relPath[i+1:]
	}
}

// matchSegments matches a compiled pattern against path segments, with '**'
// consuming any number of segments.
func matchSegments(pat, path []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(path); i++ {
				if matchSegments(pat[1:], path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		if !matchSegment(pat[0], path[0]) {
			return false
		}
		pat, path = pat[1:], path[1:]
	}
	return len(path) == 0
}

// matchSegment matches one segment against a pattern containing '*' and '?'.
func matchSegment(pat, name string) bool {
	pi, ni := 0, 0
	star, match := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pat) && (pat[pi] == name[ni] || pat[pi] == '?'):
			pi++
			ni++
		case pi < len(pat) && pat[pi] == '*':
			star = pi
			match = ni
			pi++
		case star >= 0:
			pi = star + 1
			match++
			ni = match
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}
