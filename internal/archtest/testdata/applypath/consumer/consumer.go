package consumer

func f() {}

func Run() {
	go f()
	func() {
		go f()
	}()
}

type R struct{}

func (r *R) Run() {
	go f()
}
