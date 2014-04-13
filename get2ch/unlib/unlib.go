package unlib

// 細かい便利機能

import (
	"code.google.com/p/mahonia"
	"bytes"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Stat_t struct {
	Size  int64
	Atime int64
	Mtime int64
}

func CreateModString(mod int64) string {
	return time.Unix(mod, 0).UTC().Format(http.TimeFormat)
}

func CreateDateString(mod int64) string {
	return time.Unix(mod, 0).Format("2006/01/02(Mon) 15:04:05")
}

func ShiftJISToUtf8(data []byte) []byte {
	return []byte(mahonia.NewDecoder("cp932").ConvertString(string(data)))
}
func ShiftJISToUtf8Reader(r io.Reader) io.Reader {
	return mahonia.NewDecoder("cp932").NewReader(r)
}

func Utf8ToShiftJIS(data []byte) []byte {
	buf := bytes.Buffer{}
	enc := mahonia.NewEncoder("cp932")
	enc.NewWriter(&buf).Write(data)
	return buf.Bytes()
}
func Utf8ToShiftJISWriter(w io.Writer) io.Writer {
	return mahonia.NewEncoder("cp932").NewWriter(w)
}

type RedirectError struct {
	Host string
	Path string
	Msg  string
}

func (e *RedirectError) Error() string {
	return e.Msg
}

func RedirectPolicy(r *http.Request, _ []*http.Request) error {
	return &RedirectError{r.URL.Host, r.URL.Path, "redirect error"}
}

func GetRedirectError(err error) *RedirectError {
	uerr, uok := err.(*url.Error)
	if !uok {
		return nil
	}
	rerr, rok := uerr.Err.(*RedirectError)
	if !rok {
		return nil
	}
	return rerr
}
