package directive

func F(m map[int]int, s []int) int {
	n := 0
	//evmsync:maporder sum is order-independent
	for _, v := range m {
		n += v
	}
	for _, v := range m { //evmsync:maporder sum is order-independent
		n += v
	}
	//evmsync:maporder
	for _, v := range m {
		n += v
	}
	//evmsync:maporder slices keep their order
	for _, v := range s {
		n += v
	}
	// evmsync:maporder x
	for _, v := range m {
		n += v
	}
	return n
}
