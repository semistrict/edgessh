//go:build linux

package main

import (
	"log"
	"syscall"
	"time"
)

func main() {
	syscall.Sync()
	time.Sleep(500 * time.Millisecond)
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART); err != nil {
		log.Fatal(err)
	}
}
