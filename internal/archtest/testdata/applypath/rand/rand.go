package rand

import (
	mrand "math/rand"
	"math/rand/v2"
)

func F() int {
	a := rand.IntN(3)
	b := mrand.Int()
	return a + b + rand.New(rand.NewPCG(1, 2)).IntN(3)
}
