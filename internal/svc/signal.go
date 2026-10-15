package svc

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyInterrupt(c chan os.Signal) { signal.Notify(c, os.Interrupt, syscall.SIGTERM) }
