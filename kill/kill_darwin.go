package kill

import (
	"os"
	"os/signal"
)

func CreateKillChan() <-chan os.Signal {
	killc := make(chan os.Signal, 1)
	signal.Notify(killc, os.Interrupt, os.Kill)
	return killc
}
