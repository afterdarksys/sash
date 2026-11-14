package line

import "time"

// parInterrupts is lang.Options.Interrupts for the prompt: while a par
// waits for its branches it reads the terminal, which is in raw mode, and
// sends one interrupt per Ctrl-C byte. Other bytes typed while a par runs
// are dropped. stop ends the watch and returns only once the reader has
// stopped, so the prompt's next read never races it. Under confirm, lang
// never asks for it: the confirm prompt keeps the keys.
func parInterrupts(keys keySource) func() (<-chan struct{}, func()) {
	return func() (<-chan struct{}, func()) {
		ch := make(chan struct{}, 2)
		done := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			for {
				select {
				case <-done:
					return
				default:
				}
				b, ok, err := keys.ReadByteTimeout(50 * time.Millisecond)
				if err != nil {
					return
				}
				if ok && b == 0x03 {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}()
		return ch, func() {
			close(done)
			<-stopped
		}
	}
}
