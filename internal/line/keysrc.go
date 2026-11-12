package line

import (
	"errors"
	"io"
	"syscall"
	"time"
)

// keySource is the editor's input: blocking reads for the line, and a
// read with a timeout and a discard for the confirm prompt's quiet period.
type keySource interface {
	// ReadByte blocks for the next byte.
	ReadByte() (byte, error)
	// ReadByteTimeout waits at most d; ok is false when nothing came.
	ReadByteTimeout(d time.Duration) (b byte, ok bool, err error)
	// Peek is the next byte if one is already here, without waiting.
	Peek() (byte, bool)
	// Discard drops every byte already here.
	Discard()
}

// ttySource pumps a reader (the terminal) into a channel on one
// goroutine, so a read can wait with a timeout. It lives as long as the
// process; Loop is the process's prompt.
type ttySource struct {
	ch  chan []byte
	end chan error
	buf []byte
	err error
}

func newTTYSource(r io.Reader) *ttySource {
	s := &ttySource{ch: make(chan []byte, 64), end: make(chan error, 1)}
	go func() {
		for {
			p := make([]byte, 4096)
			n, err := r.Read(p)
			if n > 0 {
				s.ch <- p[:n]
			}
			if err != nil {
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				s.end <- err
				close(s.ch)
				return
			}
		}
	}()
	return s
}

// fill makes buf non-empty: now (d < 0), within d, or blocking (d == 0).
func (s *ttySource) fill(d time.Duration) bool {
	if len(s.buf) > 0 {
		return true
	}
	if s.err != nil {
		return false
	}
	take := func(p []byte, open bool) bool {
		if !open {
			s.err = <-s.end
			return false
		}
		s.buf = p
		return true
	}
	switch {
	case d < 0:
		select {
		case p, open := <-s.ch:
			return take(p, open)
		default:
			return false
		}
	case d == 0:
		p, open := <-s.ch
		return take(p, open)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case p, open := <-s.ch:
		return take(p, open)
	case <-t.C:
		return false
	}
}

func (s *ttySource) pop() byte {
	b := s.buf[0]
	s.buf = s.buf[1:]
	return b
}

func (s *ttySource) ReadByte() (byte, error) {
	if !s.fill(0) {
		return 0, s.err
	}
	return s.pop(), nil
}

func (s *ttySource) ReadByteTimeout(d time.Duration) (byte, bool, error) {
	if d <= 0 {
		d = -1
	}
	if !s.fill(d) {
		return 0, false, s.err
	}
	return s.pop(), true, nil
}

func (s *ttySource) Peek() (byte, bool) {
	if !s.fill(-1) {
		return 0, false
	}
	return s.buf[0], true
}

func (s *ttySource) Discard() {
	s.buf = nil
	for s.fill(-1) {
		s.buf = nil
	}
}
