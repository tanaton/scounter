package unlib

import (
	"syscall"
)

func Stat(filename string) (*Stat_t, error) {
	var stat syscall.Stat_t
	err := syscall.Stat(filename, &stat)
	if err != nil {
		return nil, err
	}
	return &Stat_t{
		Size:  stat.Size,
		Atime: stat.Atim.Sec,
		Mtime: stat.Mtim.Sec,
	}, nil
}
