//go:build !windows

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"time"
)

func lockCrewFile(file *os.File) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func unlockCrewFile(file *os.File) { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }
