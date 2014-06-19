package process

import (
	"sync"
	"time"
)

const (
	BOARD_NAME_TIME = 24 * time.Hour
)

type BoardServerBox struct {
	m   map[string]string
	mux sync.RWMutex
}

func NewBoardServerBox(f func() map[string]string) *BoardServerBox {
	bs := &BoardServerBox{
		m: f(),
	}
	go func(bs *BoardServerBox) {
		c := time.Tick(1 * time.Hour)
		for _ = range c {
			bs.mux.Lock()
			bs.m = f()
			bs.mux.Unlock()
		}
	}(bs)
	return bs
}
func (bs *BoardServerBox) GetServer(board string) (server string) {
	bs.mux.RLock()
	server = bs.m[board]
	bs.mux.RUnlock()
	return
}

type boardNamePacket struct {
	board string
	name  string
}
type BoardNameBox struct {
	m   map[string]string
	wch chan<- boardNamePacket
	mux sync.RWMutex
}

func NewBoardNameBox() *BoardNameBox {
	ch := make(chan boardNamePacket, 4)
	bn := &BoardNameBox{
		m:   make(map[string]string, 1024),
		wch: ch,
	}
	go func(bn *BoardNameBox, rch <-chan boardNamePacket) {
		c := time.Tick(BOARD_NAME_TIME)
		for {
			select {
			case it := <-rch:
				bn.mux.Lock()
				bn.m[it.board] = it.name
				bn.mux.Unlock()
			case <-c:
				bn.mux.Lock()
				bn.m = make(map[string]string, 1024)
				bn.mux.Unlock()
			}
		}
	}(bn, ch)
	return bn
}
func (bn *BoardNameBox) SetName(board, bname string) {
	bnp := boardNamePacket{
		board: board,
		name:  bname,
	}
	bn.wch <- bnp
}
func (bn *BoardNameBox) GetName(board string) (name string) {
	bn.mux.RLock()
	name = bn.m[board]
	bn.mux.RUnlock()
	return
}
