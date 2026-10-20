package mapsiter

import (
	"maps"
	"slices"
)

func F(m map[int]int) int {
	n := 0
	for k := range maps.Keys(m) {
		n += k
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		n += k
	}
	return n
}
