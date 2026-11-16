package rawgoroutine

func notify(send func()) {
	go send()
	go func() {
		send()
	}()
}
