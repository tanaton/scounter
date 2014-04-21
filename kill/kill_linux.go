package kill

import (
	"os"
	"os/signal"
	"syscall"
)

func CreateKillChan() <-chan os.Signal {
	killc := make(chan os.Signal, 1)
	signal.Notify(killc, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	return killc
}
