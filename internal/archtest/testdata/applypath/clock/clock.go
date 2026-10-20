package clock

import "time"

func F(t time.Time) time.Time {
	_ = time.Now()
	_ = time.Since(t)
	f := time.Sleep
	_ = f
	_ = time.NewTimer(1)
	return t.Add(time.Second)
}
