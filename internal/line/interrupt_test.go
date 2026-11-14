package line

import (
	"io"
	"testing"
	"time"
)

// chanKeys delivers bytes as a test sends them.
type chanKeys struct{ ch chan byte }

func (k chanKeys) ReadByte() (byte, error) {
	b, ok := <-k.ch
	if !ok {
		return 0, io.EOF
	}
	return b, nil
}

func (k chanKeys) ReadByteTimeout(d time.Duration) (byte, bool, error) {
	select {
	case b, ok := <-k.ch:
		if !ok {
			return 0, false, io.EOF
		}
		return b, true, nil
	case <-time.After(d):
		return 0, false, nil
	}
}

func (k chanKeys) Peek() (byte, bool) { return 0, false }
func (k chanKeys) Discard()           {}

// Each Ctrl-C is one interrupt; other bytes are not; stop returns only
// after the reader stopped, so nothing reads the keys afterwards.
func TestParInterruptsFromKeys(t *testing.T) {
	keys := chanKeys{ch: make(chan byte, 8)}
	ch, stop := parInterrupts(keys)()
	keys.ch <- 'x'
	keys.ch <- 0x03
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no interrupt for Ctrl-C")
	}
	select {
	case <-ch:
		t.Fatal("an interrupt for a plain byte")
	case <-time.After(150 * time.Millisecond):
	}
	stop()
	keys.ch <- 0x03
	time.Sleep(120 * time.Millisecond)
	if len(keys.ch) != 1 {
		t.Fatal("the watch read a key after stop")
	}
}
