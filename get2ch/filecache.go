package get2ch

import (
	"./unlib"
	"io/ioutil"
	"os"
	"path"
	"time"
)

const (
	BOARD_SETTING       = "setting"
	tBOARD_LIST_NAME    = "ita.data"    // 板情報格納ファイル
	tBOARD_SUBJECT_NAME = "subject.txt" // スレッド一覧格納ファイル名
	tBOARD_SETTING_NAME = "setting.txt" // 板情報格納ファイル名
)

type State struct {
	fsize int64
	atime int64
	mtime int64
}

func (s *State) Size() int64 { return s.fsize }
func (s *State) Amod() int64 { return s.atime }
func (s *State) Mmod() int64 { return s.mtime }

type FileCache struct {
	Folder string // dat保管フォルダ名
}

func NewFileCache(root string) *FileCache {
	return &FileCache{
		Folder: root,
	}
}

func (fc *FileCache) Path(s, b, t string) string {
	if s == "" && b == "" && t == "" {
		return fc.Folder + "/" + tBOARD_LIST_NAME
	} else if t == BOARD_SETTING {
		return fc.Folder + "/" + b + "/" + tBOARD_SETTING_NAME
	} else if t == "" {
		return fc.Folder + "/" + b + "/" + tBOARD_SUBJECT_NAME
	}
	return fc.Folder + "/" + b + "/" + t[0:4] + "/" + t + ".dat"
}

func (fc *FileCache) GetData(s, b, t string) ([]byte, error) {
	logfile := fc.Path(s, b, t)
	return ioutil.ReadFile(logfile)
}

func (fc *FileCache) SetData(s, b, t string, d []byte) error {
	logfile := fc.Path(s, b, t)
	os.MkdirAll(path.Dir(logfile), 0777)
	return ioutil.WriteFile(logfile, d, 0777)
}

func (fc *FileCache) SetDataAppend(s, b, t string, d []byte) error {
	logfile := fc.Path(s, b, t)
	fp, err := os.OpenFile(logfile, os.O_WRONLY|os.O_APPEND, 0777)
	if err != nil {
		return err
	}
	_, err = fp.Write(d)
	fp.Close()
	return err
}

func (fc *FileCache) SetMod(s, b, t string, m, a int64) error {
	// atimeとmtimeの順番に注意
	return os.Chtimes(fc.Path(s, b, t), time.Unix(a, 0).UTC(), time.Unix(m, 0).UTC())
}

func (fc *FileCache) Exists(s, b, t string) bool {
	_, err := os.Stat(fc.Path(s, b, t))
	return err == nil
}

func (fc *FileCache) Stat(s, b, t string) (CacheState, error) {
	st, err := unlib.Stat(fc.Path(s, b, t))
	if err != nil {
		return nil, err
	}
	return &State{
		fsize: st.Size,
		atime: st.Atime,
		mtime: st.Mtime,
	}, nil
}
