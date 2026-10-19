//go:build windows

package queue

import (
	"os"
	"time"
)

// Windows has no POSIX FIFOs. The spool, results and DLQ work unchanged; only
// the wakeup is lost, so the worker relies on its ticker and picks an enqueued
// job up within maxWait()/2 rather than immediately.

func makeDoorbell(string) error { return nil }

// openDoorbell reports "no doorbell" so the worker uses its ticker.
func openDoorbell(string) (*os.File, error) { return nil, nil }

func waitDoorbell(*os.File, time.Duration) {}

func ringDoorbell(string) {}
