package maprange

type C interface{ ~map[string]int }

func F(m map[int]int, s []int) int {
	n := 0
	for k := range m {
		n += k
	}
	for range s {
		n++
	}
	return n
}

func G[M ~map[int]int](m M) {
	for range m {
	}
}

func H[T C](m T) {
	for range m {
	}
}
