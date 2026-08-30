package model

import "slices"

// slicesSortFunc is a thin alias kept so the sorting helper used by both the
// builder and the query paths lives in one place.
func slicesSortFunc[S ~[]E, E any](s S, cmp func(a, b E) int) { slices.SortFunc(s, cmp) }
