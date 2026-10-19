//go:build unix

package queue

import (
	"errors"
	"math"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// makeDoorbell creates the FIFO used to wake the worker.
func makeDoorbell(path string) error {
	if err := syscall.Mkfifo(path, 0o600); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	return nil
}

// openDoorbell opens the FIFO read end without blocking. A nil file means the
// doorbell is unavailable, and the worker falls back to its ticker.
func openDoorbell(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: path is the queue directory's doorbell.
	if err != nil {
		return nil, err
	}
	return f, nil
}

// waitDoorbell blocks for up to timeout waiting for a ring, then drains it.
func waitDoorbell(f *os.File, timeout time.Duration) {
	fd := f.Fd()
	// unix.PollFd.Fd is an int32, while File.Fd is a uintptr. A descriptor that
	// does not fit would be silently truncated and poll a different descriptor,
	// so bounds-check it and wait out the timeout instead.
	if fd > math.MaxInt32 {
		time.Sleep(timeout)
		return
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, _ := unix.Poll(fds, int(timeout/time.Millisecond))
	if n <= 0 {
		return
	}
	buf := make([]byte, 64)
	for {
		if _, err := f.Read(buf); err != nil {
			break
		}
	}
}

// ringDoorbell writes one wakeup byte into the FIFO.
func ringDoorbell(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: path is the queue directory's doorbell.
	if err != nil {
		return // no reader yet; the ticker will pick it up
	}
	_, _ = f.Write([]byte{1})
	_ = f.Close()
}
