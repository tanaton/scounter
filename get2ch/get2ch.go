package get2ch

import (
	"./unlib"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	CONF_FOLDER      = "/2ch_sc/dat" // dat保管フォルダ名
	CONF_ITAURL_HOST = "menu.2ch.sc" // 板情報取得URL
	CONF_ITAURL_FILE = "bbsmenu.html"

	BOURBON_TIME    time.Duration = 1 * time.Minute
	BOARD_NAME_TIME time.Duration = 24 * time.Hour

	DAT_MAX_SIZE              = 614400 * 5
	DAT_NOT_REQUEST_RES_COUNT = 1000 * 10  // 10000レスを超えていたらリクエストしないようにする
	DAT_NOT_SIZE_LIMIT        = 524288 * 5 // しきい値

	FILE_SUBJECT_TXT     = "subject.txt"
	FILE_SUBJECT_TXT_REQ = "subject.txt"
	FILE_SETTING_TXT     = "setting.txt"
	FILE_SETTING_TXT_REQ = "SETTING.TXT"

	VIEW_THREAD_LIST_SIZE = 100

	TIMEOUT_SEC time.Duration = 12 * time.Second

	USER_AGENT = "Monazilla/1.00 (scounter)"
)

const (
	DAT_CREATE = iota
	DAT_APPEND
	DAT_BOURBON_THREAD
	DAT_BOURBON_BOARD
)

type CacheState interface {
	Size() int64
	Amod() int64
	Mmod() int64
}

type Cache interface {
	Path(s, b, t string) string
	GetData(s, b, t string) ([]byte, error)
	SetData(s, b, t string, d []byte) error
	SetDataAppend(s, b, t string, d []byte) error
	SetMod(s, b, t string, m, a int64) error
	Exists(s, b, t string) bool
	Stat(s, b, t string) (CacheState, error)
}

type Salami struct {
	Host string
	Port int
}

type Get2ch struct {
	size      int64 // datのデータサイズ
	mod       int64 // datの最終更新時間
	cache_mod int64 // datの最終更新時間
	code      int   // HTTPステータスコード
	err       error // エラーメッセージ
	server    string
	board     string
	thread    string
	req_time  int64
	cache     Cache
	numlines  int // 行数
	salami    string
}

var catekill = map[string]bool{
	"特別企画":        true,
	"チャット":        true,
	"他のサイト":       true,
	"まちＢＢＳ":       true,
	"ツール類":        true,
	"チャット２ｃｈ＠ＩＲＣ": true,
	"Top10":       true,
	"2chのゴミ箱":     true,
	"BBSPINKのゴミ箱": true,
}

var sabakill = map[string]bool{
	"www.2ch.sc":          true,
	"info.2ch.sc":         true,
	"find.2ch.sc":         true,
	"v.isp.2ch.sc":        true,
	"m.2ch.sc":            true,
	"test.up.bbspink.com": true,
	"stats.2ch.sc":        true,
	"c-au.2ch.sc":         true,
	"c-others1.2ch.sc":    true,
	"movie.2ch.sc":        true,
	"img.2ch.sc":          true,
	"ipv6.2ch.sc":         true,
	"be.2ch.sc":           true,
	"p2.2ch.sc":           true,
	"shop.2ch.sc":         true,
	"watch.2ch.sc":        true,
}

type hideData struct {
	server string
	name   string
}

var RegServerItem = regexp.MustCompile(`<B>([^<]+)<\/B>`)
var RegServer = regexp.MustCompile(`<A HREF=http:\/\/([^\/]+)\/([^\/]+)\/>([^<]+)<\/A>`)

type boardServerPacket struct {
	board string
	rch   chan<- string
}

type boardNamePacket struct {
	board string
	name  string
	rch   chan<- string
}

var g_once sync.Once
var boardServerCh chan<- boardServerPacket
var boardNameCh chan<- boardNamePacket
var g_cache Cache
var g_salami string
var g_started bool
var tanpanman = []byte{0x92, 0x5A, 0x83, 0x70, 0x83, 0x93, 0x83, 0x7d, 0x83, 0x93, 0x20, 0x81, 0x9a}
var nagoyaee = []byte{0x96, 0xBC, 0x8C, 0xC3, 0x89, 0xAE, 0x82, 0xCD, 0x83, 0x47, 0x81, 0x60, 0x83, 0x47, 0x81, 0x60, 0x82, 0xC5}

// get2ch管理機能の起動
// 使用を開始する前に呼び出すこと
func Start(c Cache, s *Salami) {
	// サーバリスト更新
	g_once.Do(func() {
		SetCache(c)
		SetSalami(s)
		boardServerCh = boardServerListProc()
		boardNameCh = boardNameProc()
		g_started = true
	})
}

func SetSalami(s *Salami) {
	if s != nil {
		g_salami = fmt.Sprintf("%s:%d/", s.Host, s.Port)
	} else {
		g_salami = ""
	}
}

func SetCache(c Cache) {
	if c != nil {
		g_cache = c
	}
}

func boardServerListProc() chan<- boardServerPacket {
	wch := make(chan boardServerPacket, 4)
	go func(wch <-chan boardServerPacket) {
		m := setServerList()
		c := time.Tick(10 * time.Minute)
		for {
			select {
			case <-c:
				m = setServerList()
			case it := <-wch:
				if it.rch != nil {
					it.rch <- m[it.board]
				}
			}
		}
	}(wch)
	return wch
}

func boardNameProc() chan<- boardNamePacket {
	reqch := make(chan boardNamePacket, 4)
	go func(reqch <-chan boardNamePacket) {
		m := make(map[string]string)
		for it := range reqch {
			if it.rch != nil {
				// 返信
				it.rch <- m[it.board]
			} else {
				m[it.board] = it.name
			}
		}
	}(reqch)
	return reqch
}

func NewGet2ch(board, thread string) *Get2ch {
	if g_started == false {
		return nil
	}
	g2ch := &Get2ch{
		size:      0,
		mod:       0,
		cache_mod: 0,
		code:      0,
		err:       nil,
		server:    "",
		board:     "",
		thread:    "",
		req_time:  time.Now().Unix(),
		cache:     g_cache,
		numlines:  0,
		salami:    g_salami,
	}
	g2ch.server = g2ch.GetServer(board)
	g2ch.board = board
	if _, err := strconv.ParseInt(thread, 10, 64); err == nil {
		g2ch.thread = thread
	}
	return g2ch
}

func (g2ch *Get2ch) GetData() (data []byte, err error) {
	// 初期化
	g2ch.size = 0
	g2ch.mod = 0
	g2ch.cache_mod = 0
	g2ch.code = 0
	g2ch.err = nil
	g2ch.numlines = 0

	// 通常取得
	data = g2ch.normalData(true)

	err = g2ch.err
	// SJIS-winで返す
	return
}

func (g2ch *Get2ch) GetByteSize() int64 {
	return g2ch.size
}

func (g2ch *Get2ch) GetModified() int64 {
	return g2ch.mod
}

func (g2ch *Get2ch) GetHttpCode() int {
	return g2ch.code
}

func (g2ch *Get2ch) GetError() error {
	return g2ch.err
}

func (g2ch *Get2ch) NumLines(data []byte) int {
	if g2ch.numlines == 0 {
		g2ch.numlines = bytes.Count(data, []byte{'\n'})
	}
	return g2ch.numlines
}

func (g2ch *Get2ch) isThread() bool {
	return g2ch.server != "" && g2ch.board != "" && g2ch.thread != ""
}

func (g2ch *Get2ch) isBoard() bool {
	return g2ch.server != "" && g2ch.board != "" && g2ch.thread == ""
}

func dialTimeout(network, addr string) (net.Conn, error) {
	con, err := net.DialTimeout(network, addr, TIMEOUT_SEC)
	if err == nil {
		con.SetDeadline(time.Now().Add(TIMEOUT_SEC))
	}
	return con, err
}

func newHttpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Dial:                  dialTimeout,
			DisableKeepAlives:     true,
			DisableCompression:    true, // 圧縮解凍は全てこっちで指示する
			ResponseHeaderTimeout: TIMEOUT_SEC,
		},
		CheckRedirect: unlib.RedirectPolicy,
	}
}

func responseRead(resp *http.Response) (data []byte, err error) {
	var r io.Reader
	var gz io.ReadCloser

	ce := resp.Header.Get("Content-Encoding")
	if ce == "gzip" {
		// 解凍する
		gz, _ = gzip.NewReader(resp.Body)
		r = gz
	} else {
		// 圧縮されていない場合
		r = resp.Body
	}
	data, err = ioutil.ReadAll(io.LimitReader(r, DAT_MAX_SIZE))
	if gz != nil {
		gz.Close()
	}
	return
}

func getHttpBBSmenu(cache Cache) (data []byte, mod int64, err error) {
	// header生成
	req, nrerr := http.NewRequest("GET", "http://"+g_salami+CONF_ITAURL_HOST+"/"+CONF_ITAURL_FILE, nil)
	if nrerr != nil {
		return nil, 0, nrerr
	}
	req.Header.Set("User-Agent", USER_AGENT)
	// 更新確認
	if st, merr := cache.Stat("", "", ""); merr == nil {
		req.Header.Set("If-Modified-Since", unlib.CreateModString(st.Mmod()))
	}
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Connection", "close")
	resp, doerr := newHttpClient().Do(req)
	if doerr != nil {
		return nil, 0, doerr
	}
	defer resp.Body.Close()

	code := resp.StatusCode
	if t, lerr := http.ParseTime(resp.Header.Get("Last-Modified")); lerr == nil {
		mod = t.Unix()
	} else {
		mod = time.Now().Unix()
	}

	if code == 200 {
		// レスポンスボディをラップする
		data, err = responseRead(resp)
	} else {
		err = errors.New("更新されていません")
	}
	return
}

// 板一覧取得
func saveBBSmenu(cache Cache) []byte {
	d, mod, err := getHttpBBSmenu(cache)
	if err != nil {
		// errがnil以外の時、rcはnil
		return nil
	}

	// これ以降はUTF-8
	data := bytes.Buffer{}
	scanner := bufio.NewScanner(unlib.ShiftJISToUtf8Reader(bytes.NewReader(d)))
	for scanner.Scan() {
		line := scanner.Text()
		if match := RegServerItem.FindStringSubmatch(line); match != nil {
			// 当てはまるものを除外
			if _, ok := catekill[match[1]]; !ok {
				data.WriteString(match[1] + "\n")
			}
		} else if strings.Contains(line, ".2ch.sc/") {
			if strings.Contains(line, "TARGET") {
				continue
			}
			if match := RegServer.FindStringSubmatch(line); match != nil {
				server := match[1]
				board := match[2]
				title := match[3]
				if _, ok := sabakill[server]; ok {
					continue
				}
				data.WriteString(server + "/" + board + "<>" + title + "\n")
			}
		}
	}
	// ファイルにはUTF-8で保存
	cache.SetData("", "", "", data.Bytes())
	cache.SetMod("", "", "", mod, mod)
	return data.Bytes()
}

func (g2ch *Get2ch) GetBBSmenu(flag bool) (data []byte) { // trueがデフォルト
	if g2ch.cache.Exists("", "", "") == false {
		// 存在しない場合取得する
		data = saveBBSmenu(g2ch.cache)
	}
	if flag {
		if st, err := g2ch.cache.Stat("", "", ""); err == nil {
			g2ch.mod = st.Mmod()
		}
	}
	if data == nil {
		var err error
		data, err = g2ch.cache.GetData("", "", "")
		if err != nil {
			data = nil
		}
	}
	return
}

func (g2ch *Get2ch) GetServer(board_key string) string {
	retdata := ""
	if board_key == "" {
		retdata = g2ch.server
	} else {
		ch := make(chan string, 1) // バッファが無いと低速？
		boardServerCh <- boardServerPacket{
			board: board_key,
			rch:   ch,
		}
		retdata = <-ch
		close(ch)
	}
	return retdata
}

func setServerList() map[string]string {
	m := make(map[string]string, 1024)
	cache := g_cache
	data := saveBBSmenu(cache)
	if data == nil {
		var err error
		data, err = cache.GetData("", "", "")
		if err != nil {
			return m
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		sp := strings.Split(scanner.Text()+"<>", "<>")
		dat, name := sp[0], sp[1]
		if name != "" {
			u := strings.Split(dat, "/")
			server, board := u[0], u[1]
			if _, ok := m[board]; !ok {
				// 存在しなかったらセットする
				m[board] = server
			}
		}
	}
	return m
}

func getBoardNameSub(bd string) string {
	data, err := g_cache.GetData("", "", "")
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		sp := strings.Split(scanner.Text()+"<>", "<>")
		dat, name := sp[0], sp[1]
		if name != "" {
			u := strings.Split(dat, "/")
			board := u[1]
			if bd == board {
				return name
			}
		}
	}
	return ""
}

// 板名取得
func (g2ch *Get2ch) GetBoardName() (boardname string) {
	ch := make(chan string, 1) // バッファが無いと低速？
	rbnp := boardNamePacket{
		board: g2ch.board,
		rch:   ch,
	}
	// 板名マップの探索
	boardNameCh <- rbnp
	// 受信
	boardname = <-ch
	close(ch)

	if boardname == "" {
		boardname = g2ch.sliceBoardName()
		if boardname == "" {
			boardname = getBoardNameSub(g2ch.board)
		}

		bnp := boardNamePacket{
			board: g2ch.board,
			name:  boardname,
		}
		// 空白でも登録
		boardNameCh <- bnp
	}
	return
}

func (g2ch *Get2ch) getSettingFile() ([]byte, error) {
	server := g2ch.server
	board := g2ch.board
	req_time := g2ch.req_time

	var cf bool
	// 未来の時間
	if st, err := g2ch.cache.Stat(server, board, BOARD_SETTING); err == nil {
		cf = (st.Mmod() > req_time)
	}
	if cf {
		// Cacheを返す
		// UTF-8に変換
		cdata, err := g2ch.cache.GetData(server, board, BOARD_SETTING)
		if err != nil {
			cdata = []byte{}
		}
		return unlib.ShiftJISToUtf8(cdata), nil
	}

	// header生成
	req, nrerr := http.NewRequest("GET", "http://"+g2ch.salami+server+"/"+board+"/"+FILE_SETTING_TXT_REQ, nil)
	if nrerr != nil {
		return nil, nrerr
	}
	req.Header.Set("User-Agent", USER_AGENT)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Connection", "close")
	resp, doerr := newHttpClient().Do(req)
	if doerr != nil {
		return nil, doerr
	}
	defer resp.Body.Close()

	var data []byte
	var err error
	code := resp.StatusCode
	if code == 200 {
		// 読み込む
		if data, err = responseRead(resp); err == nil {
			g2ch.cache.SetData(server, board, BOARD_SETTING, data)
			mod := req_time + (3600 * 24 * 7)
			g2ch.cache.SetMod(server, board, BOARD_SETTING, mod, mod)
		}
	} else {
		// 板名取得失敗
		// 特にエラーとしない
		if data, err = g2ch.cache.GetData(server, board, BOARD_SETTING); err != nil {
			// ファイルが存在しない場合
			data = []byte{}
		}
	}
	// 返す際にUTF-8に変換
	return unlib.ShiftJISToUtf8(data), nil
}

func (g2ch *Get2ch) sliceBoardName() (bname string) {
	stf, err := g2ch.getSettingFile()
	if err != nil {
		return
	}
	start_text := []byte("BBS_TITLE=")
	if start := bytes.Index(stf, start_text); start >= 0 {
		start += len(start_text)
		if end := bytes.IndexByte(stf[start:], '\n'); end >= 0 {
			stf = stf[start : start+end]
			var name string
			if i := bytes.Index(stf, []byte("＠")); i >= 0 {
				name = string(stf[:i])
			} else {
				name = string(stf)
			}
			bname = strings.Trim(name, " \t")
		}
	}
	return
}

// header送信
func (g2ch *Get2ch) request(flag bool) (data []byte) {
	var req *http.Request
	var err error
	server := g2ch.server
	board := g2ch.board
	thread := g2ch.thread
	req_time := g2ch.req_time

	if server == "" {
		// サーバが分からない
		g2ch.code = 302
		return
	} else if g2ch.isThread() {
		// dat取得用header生成
		req, err = http.NewRequest("GET", "http://"+g2ch.salami+server+"/"+board+"/dat/"+thread+".dat", nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", USER_AGENT)

		st, err := g2ch.cache.Stat(server, board, thread)
		if flag && err == nil {
			size := st.Size()
			if size > 1 {
				// 1バイト引いても差分取得ができる場合
				// 1バイト引いて取得する
				req.Header.Set("Range", "bytes="+strconv.Itoa(int(size-1))+"-")
			}
			req.Header.Set("If-Modified-Since", unlib.CreateModString(st.Mmod()))
		} else {
			// 差分取得は使えないためここで設定
			req.Header.Set("Accept-Encoding", "gzip")
		}
	} else if g2ch.isBoard() {
		// スレッド一覧取得用header生成
		req, err = http.NewRequest("GET", "http://"+g2ch.salami+server+"/"+board+"/"+FILE_SUBJECT_TXT_REQ, nil)
		if err != nil {
			g2ch.err = err
			return
		}
		req.Header.Set("User-Agent", USER_AGENT)

		if st, err := g2ch.cache.Stat(server, board, ""); err == nil {
			req.Header.Set("If-Modified-Since", unlib.CreateModString(st.Mmod()))
		}
		req.Header.Set("Accept-Encoding", "gzip")
	} else {
		g2ch.err = errors.New("スレッドもしくは板ではありません。")
		g2ch.code = 0
		return
	}
	req.Header.Set("Connection", "close")

	// リクエスト送信
	var resp *http.Response
	resp, err = newHttpClient().Do(req)
	if err != nil {
		// errがnil以外の場合、resp.Bodyは閉じられている
		if resp == nil {
			g2ch.err = err
			g2ch.code = 0
		} else {
			if rerr := unlib.GetRedirectError(err); rerr != nil {
				// RedirectErrorだった場合は処理続行
				g2ch.code = resp.StatusCode
			} else {
				g2ch.err = err
				g2ch.code = 0
			}
		}
		// 終了
		return
	}
	defer resp.Body.Close()

	// 読み込み
	data, err = responseRead(resp)
	if err != nil {
		g2ch.err = err
		g2ch.code = 0
		return nil
	}

	g2ch.code = resp.StatusCode
	g2ch.size = int64(len(data))
	mod := int64(0)
	if t, perr := http.ParseTime(resp.Header.Get("Last-Modified")); perr == nil {
		mod = t.Unix()
	}

	if g2ch.code == 304 {
		// データは空
		data = []byte{}
	} else if flag && (g2ch.code == 206) && (g2ch.size > 1) {
		// あぼーん検知
		data = lfCheck(data)
		if data == nil {
			g2ch.code = 416
		}
	}
	if mod != 0 {
		g2ch.cache_mod = mod
	} else {
		g2ch.cache_mod = req_time
	}
	return
}

func (g2ch *Get2ch) normalData(reget bool) []byte {
	var err error
	// データ取得
	data := g2ch.request(reget)
	if g2ch.isThread() {
		switch g2ch.code {
		case 200:
			g2ch.createCache(data, DAT_CREATE)
		case 206:
			g2ch.createCache(data, DAT_APPEND)
			data, err = g2ch.readThread()
			if err != nil {
				data = g2ch.dataErrorDat()
			}
		case 416:
			if reget {
				// もう一回取得
				data = g2ch.normalData(false)
			} else {
				data, err = g2ch.readThread()
				if err != nil {
					data = g2ch.dataErrorDat()
				}
			}
		case 301, 302, 404:
			if st, staterr := g2ch.cache.Stat(g2ch.server, g2ch.board, g2ch.thread); staterr == nil {
				g2ch.size = st.Size()
				g2ch.mod = st.Mmod()
				if g2ch.size < DAT_MAX_SIZE {
					data, _ = g2ch.cache.GetData(g2ch.server, g2ch.board, g2ch.thread)
				} else {
					data = g2ch.dataError()
				}
			} else {
				data = g2ch.dataErrorDat()
			}
		default:
			// キャッシュ利用
			data, err = g2ch.readThread()
			if err != nil {
				data = g2ch.dataErrorDat()
			}
		}
	} else {
		switch g2ch.code {
		case 200:
			g2ch.createCache(data, DAT_CREATE)
		case 301, 302, 404:
			// 鯖情報取得
			saveBBSmenu(g2ch.cache)
			data = []byte{}
			g2ch.err = errors.New("２ちゃんねるにアクセスできなかったので、サーバー移転チェックを行いました。")
		default:
			// キャッシュ利用
			data, err = g2ch.readBoard()
			if err != nil {
				data = g2ch.dataErrorDat()
			}
		}
	}
	return data
}

func (g2ch *Get2ch) dataError() []byte {
	data := bytes.Buffer{}
	g2ch.err = errors.New("壊れているため表示できません。")
	if g2ch.isThread() {
		data.WriteString("unkar.org<><>")
		data.WriteString(unlib.CreateDateString(g2ch.req_time))
		data.WriteString("<>DATが壊れているため表示できません。<>なんかえらーだって\n")
	} else {
		data.WriteString(strconv.Itoa(int(g2ch.req_time)))
		data.WriteString(".dat<>板が壊れているため表示できません (1)\n")
	}
	return unlib.Utf8ToShiftJIS(data.Bytes())
}

func (g2ch *Get2ch) dataErrorDat() []byte {
	data := bytes.Buffer{}
	g2ch.err = errors.New("アクセス不可(dat落ち)")
	if g2ch.isThread() {
		data.WriteString("unkar.org<><>")
		data.WriteString(unlib.CreateDateString(g2ch.req_time))
		data.WriteString("<>スレッドを発見できませんでした。dat落ちのようです。<>アクセス不可(dat落ち)\n")
	} else {
		data.WriteString(strconv.Itoa(int(g2ch.req_time)))
		data.WriteString(".dat<>２ちゃんねるにアクセスできませんでした。 (1)\n")
	}
	return unlib.Utf8ToShiftJIS(data.Bytes())
}

// 必ずSJIS-winの状態で渡す
func lfCheck(data []byte) []byte {
	// ソースはUTF-8で文字列はSJIS-win
	// 改行コードはASCIIの範囲なので問題なし
	if data[0] == '\n' {
		return data[1:]
	}
	return nil
}

// 必ずSJIS-winの状態で渡す
func (g2ch *Get2ch) createCache(data []byte, switch_data int) error {
	mod := g2ch.cache_mod
	append_data := false
	renew := true

	if data == nil {
		return errors.New("data nil")
	}

	switch switch_data {
	case DAT_CREATE:
		append_data = false
	case DAT_APPEND:
		if len(data) > 0 {
			// データが存在するので追記
			append_data = true
		} else {
			// データが更新されていない
			renew = false
		}
	case DAT_BOURBON_THREAD:
		append_data = false
		if st, err := g2ch.cache.Stat(g2ch.server, g2ch.board, g2ch.thread); err == nil {
			if int64(len(data)) <= st.Size() {
				// データが更新されていない
				renew = false
			}
		}
		break
	default:
		// 何もしない
		break
	}

	if renew {
		// バーボン中ではない、またはデータが更新されている場合
		// ファイルに書き込む
		if append_data {
			// 追記する
			g2ch.cache.SetDataAppend(g2ch.server, g2ch.board, g2ch.thread, data) // 追記
		} else {
			g2ch.cache.SetData(g2ch.server, g2ch.board, g2ch.thread, data) // 上書き
		}
		// If-Modified-Sinceをセット
		if mod != 0 {
			g2ch.cache.SetMod(g2ch.server, g2ch.board, g2ch.thread, mod, mod)
			g2ch.mod = mod
		}
	} else {
		if mod != 0 {
			g2ch.mod = mod
			g2ch.cache.SetMod(g2ch.server, g2ch.board, g2ch.thread, mod, mod)
		}
	}
	return nil
}

func (g2ch *Get2ch) readThread() (data []byte, err error) {
	var st CacheState
	if st, err = g2ch.cache.Stat(g2ch.server, g2ch.board, g2ch.thread); err == nil {
		g2ch.size = st.Size()
		g2ch.mod = st.Mmod()
		data, err = g2ch.cache.GetData(g2ch.server, g2ch.board, g2ch.thread)
	}
	return
}

func (g2ch *Get2ch) readBoard() (data []byte, err error) {
	var st CacheState
	if st, err = g2ch.cache.Stat(g2ch.server, g2ch.board, ""); err == nil {
		g2ch.size = st.Size()
		g2ch.mod = st.Mmod()
		data, err = g2ch.cache.GetData(g2ch.server, g2ch.board, "")
	}
	return
}
