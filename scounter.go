package main

import (
	"./get2ch"
	"./kill"
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Nich struct {
	server string
	board  string
	thread string
}

type ScCountPacket struct {
	board string
	item  map[int64]*SaveItem
}

type SaveItem struct {
	Count  int
	Thread int
	Id     map[string]int
}

const (
	GO_THREAD_SLEEP_TIME   = 2 * time.Second
	GO_BOARD_SLEEP_TIME    = 5 * time.Second
	GO_NETERROR_SLEEP_TIME = 1 * time.Minute
	SERVER_LIST_INTERVAL   = 10 * time.Minute
	DAY                    = time.Hour * 24
	ROOT_PATH              = "/2ch_sc/dat"
	COUNT_PATH             = "/2ch_sc/scount.json"
)

var g_reg_bbs = regexp.MustCompile(`(.+\.2ch\.sc)/(.+)<>`)
var g_reg_dat = regexp.MustCompile(`^(.+)\.dat<>`)
var g_reg_res = regexp.MustCompile(` \(([0-9]+)\)$`)
var g_reg_date = regexp.MustCompile(`(\d{1,4})\/(\d{1,2})\/(\d{1,2})`)
var g_reg_id = regexp.MustCompile(` ID:([\w!\+/]+)`)
var g_cache = get2ch.NewFileCache(ROOT_PATH)

var gScCountCh chan<- *ScCountPacket
var gLogger = log.New(os.Stdout, "", log.LstdFlags)

var g_filter map[string]bool = map[string]bool{
	"ipv6.2ch.sc":     true,
	"headline.2ch.sc": true,
}

func init() {
	gScCountCh = scCountProc()
}

func main() {
	go func() {
		gLogger.Println(http.ListenAndServe("localhost:6061", nil))
	}()
	// get2ch開始
	get2ch.Start(g_cache, nil)
	// 今までのキャッシュを読み込み
	loadRes()
	nsl := getServer()
	sl := copyServerList(nsl)
	// クローラーの立ち上げ
	killch := startCrawler(sl)

	tick := time.Tick(SERVER_LIST_INTERVAL)
	for _ = range tick {
		// 10分毎に板一覧を更新
		gLogger.Printf("Update server list\n")
		// 更新
		nsl = getServer()

		var flag bool
		if len(sl) != len(nsl) {
			// 鯖が増減した
			flag = true
		} else {
			for key, _ := range nsl {
				if _, ok := sl[key]; !ok {
					flag = true
					break
				}
			}
		}

		if flag {
			// 今のクローラーを殺す
			close(killch)
			// 鯖を更新
			sl = copyServerList(nsl)
			// 新クローラーの立ち上げ
			killch = startCrawler(sl)
		}
	}
}

func copyServerList(sl map[string][]Nich) (ret map[string][]Nich) {
	ret = make(map[string][]Nich, len(sl))
	for key, it := range sl {
		tmp := make([]Nich, len(it))
		copy(tmp, it)
		ret[key] = tmp
	}
	return
}

func startCrawler(sl map[string][]Nich) (killch chan struct{}) {
	// 新クローラー立ち上げ
	gLogger.Printf("========== startCrawler start ==========\n")
	killch = make(chan struct{})
	for key, it := range sl {
		gLogger.Printf("Server:%s, Board_len:%d\n", key, len(it))
		go mainThread(key, it, killch)
		time.Sleep(GO_THREAD_SLEEP_TIME)
	}
	gLogger.Printf("========== startCrawler end ==========\n")
	return
}

func loadRes() {
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		loadResDay(now.Add(DAY * time.Duration(i) * -1))
	}
}

func loadResDay(t time.Time) {
	path := createPath(t)
	fp, err := os.Open(path)
	if err != nil {
		return
	}
	defer fp.Close()
	date := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	r := bufio.NewReader(fp)
	data := map[string]*SaveItem{}
	json.NewDecoder(r).Decode(&data)

	// ロードする
	for board, si := range data {
		sc := &ScCountPacket{
			board: board,
			item:  make(map[int64]*SaveItem, 1),
		}
		sc.item[date.Unix()] = si
		gScCountCh <- sc
	}
}

func checkOpen(ch <-chan struct{}) bool {
	select {
	case <-ch:
		// chanがクローズされると即座にゼロ値が返ることを利用
		return false
	default:
		break
	}
	return true
}

func mainThread(key string, bl []Nich, killch chan struct{}) {
	for {
		for _, nich := range bl {
			// 板の取得
			tl, err := getBoard(nich)
			if err == nil {
				if len(tl) > 0 {
					// スレッドの取得
					getThread(tl, nich.board, killch)
				}
			} else {
				// エラーが発生した場合
				// とりあえず待機
				time.Sleep(GO_NETERROR_SLEEP_TIME)
			}
			if checkOpen(killch) == false {
				// 緊急停止
				break
			}
			// 少し待機
			time.Sleep(GO_BOARD_SLEEP_TIME)
		}
		if checkOpen(killch) == false {
			// 緊急停止
			break
		}
		// 少し待機
		time.Sleep(GO_BOARD_SLEEP_TIME)
	}
}

func getServer() map[string][]Nich {
	var nich Nich
	get := get2ch.NewGet2ch("", "")
	sl := make(map[string][]Nich, 16)
	// 更新時間を取得しない
	data := get.GetBBSmenu(false)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if d := g_reg_bbs.FindStringSubmatch(scanner.Text()); len(d) > 0 {
			nich.server = d[1]
			nich.board = d[2]
			if _, ok := g_filter[nich.server]; ok {
				continue
			}
			nl, ok := sl[nich.server]
			if !ok {
				nl = make([]Nich, 0, 32)
			}
			sl[nich.server] = append(nl, nich)
		}
	}
	// 余分な領域を削る
	for board, it := range sl {
		l := len(it)
		sl[board] = it[:l:l]
	}
	return sl
}

func getBoard(nich Nich) ([]Nich, error) {
	h := threadResList(nich)
	get := get2ch.NewGet2ch(nich.board, "")
	data, err := get.GetData()
	if err != nil {
		gLogger.Println(err)
		gLogger.Println(nich)
		gLogger.Println(get)
		return nil, err
	}
	code := get.GetHttpCode()
	gLogger.Printf("%d %s/%s\n", code, nich.server, nich.board)
	if code != 200 {
		err := get.GetError()
		gLogger.Println(err)
		return nil, err
	}

	var n Nich
	vect := make([]Nich, 0, 32)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		it := scanner.Text()
		if da := g_reg_dat.FindStringSubmatch(it); da != nil {
			if d := g_reg_res.FindStringSubmatch(it); d != nil {
				n.server = nich.server
				n.board = nich.board
				n.thread = da[1]
				if m, ok := h[da[1]]; ok {
					if j, err := strconv.Atoi(d[1]); err == nil && m != j {
						vect = append(vect, n)
					}
				} else {
					vect = append(vect, n)
				}
			}
		}
	}
	l := len(vect)
	return vect[:l:l], nil
}

func threadResList(nich Nich) map[string]int {
	data, err := g_cache.GetData(nich.server, nich.board, "")
	h := make(map[string]int, 1024)
	if err != nil {
		return h
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		it := scanner.Text()
		if da := g_reg_dat.FindStringSubmatch(it); da != nil {
			if d := g_reg_res.FindStringSubmatch(it); d != nil {
				m, _ := strconv.Atoi(d[1])
				h[da[1]] = m
			}
		}
	}
	return h
}

func getThread(tl []Nich, board string, killch chan struct{}) {
	sc := &ScCountPacket{
		board: board,
		item:  make(map[int64]*SaveItem, 5),
	}
	for _, nich := range tl {
		var size int64
		st, err := g_cache.Stat(nich.server, nich.board, nich.thread)
		if err == nil {
			size = st.Size()
		}
		get := get2ch.NewGet2ch(nich.board, nich.thread)
		data, err := get.GetData()
		if err != nil {
			gLogger.Println(err)
			gLogger.Printf("%s/%s/%s\n", nich.server, nich.board, nich.thread)
		} else {
			code := get.GetHttpCode()
			if (code/100) == 2 && int64(len(data)) > size {
				// カウント処理
				scCount(data[size:], sc, code == 200 && size == 0)
				gLogger.Printf("%d OK %s/%s/%s\n", code, nich.server, nich.board, nich.thread)
			} else {
				gLogger.Printf("%d NG %s/%s/%s\n", code, nich.server, nich.board, nich.thread)
			}
		}
		if checkOpen(killch) == false {
			// 緊急停止
			break
		}
	}
	if len(sc.item) > 0 {
		// 集計
		gScCountCh <- sc
	}
}

func scCount(data []byte, sc *ScCountPacket, newth bool) {
	lineno := 0
	before := time.Now().UTC().Add(DAY * 3 * -1)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		lineno++
		list := strings.Split(scanner.Text(), "<>")
		if len(list) < 3 {
			continue
		}
		if strings.Contains(list[2], ".net") == false {
			// scのレス
			m := g_reg_date.FindStringSubmatch(list[2])
			if m == nil {
				continue
			}
			t := convertTime(m[1:])
			if before.Before(t) {
				// scの書き込み
				u := t.Unix()
				ti, ok := sc.item[u]
				if !ok {
					ti = createSaveItem()
					sc.item[u] = ti
				}
				ti.Count += 1

				if id := g_reg_id.FindStringSubmatch(list[2]); id != nil {
					// ID付き
					ti.Id[id[1]] += 1
				}
				if newth && lineno == 1 {
					// scで立てられたスレッド
					ti.Thread++
				}
			}
		}
	}
}

func convertTime(datelist []string) (t time.Time) {
	if len(datelist) < 3 {
		return
	}
	y, err := strconv.Atoi(datelist[0])
	if err != nil {
		return
	}
	mtmp, err := strconv.Atoi(datelist[1])
	if err != nil || mtmp > 12 || mtmp < 1 {
		return
	}
	m := time.Month(mtmp)
	d, err := strconv.Atoi(datelist[2])
	if err != nil {
		return
	}
	t = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return
}

func scCountProc() chan<- *ScCountPacket {
	ch := make(chan *ScCountPacket, 32)
	go func(ch <-chan *ScCountPacket) {
		wc := time.Tick(time.Minute * 3)
		delc := time.Tick(time.Hour * 12)
		killc := kill.CreateKillChan()
		m := make(map[int64]map[string]*SaveItem, 3)
		for {
			select {
			case <-killc:
				// 終了時にファイルに書き出し
				storeData(m)
				// 強制終了
				os.Exit(1)
			case <-wc:
				// 3分毎にファイルに書き出し
				storeData(m)
			case now := <-delc:
				// 3日以上前のデータを削除
				unix := now.UTC().Add(DAY * 3 * -1).Unix()
				dl := []int64{}
				for key, _ := range m {
					if key < unix {
						dl = append(dl, key)
					}
				}
				for _, it := range dl {
					delete(m, it)
				}
			case sc := <-ch:
				// データの追加
				for u, item := range sc.item {
					var bsi *SaveItem
					si, ok := m[u]
					if ok {
						bsi, ok = si[sc.board]
						if !ok {
							bsi = createSaveItem()
							si[sc.board] = bsi
						}
					} else {
						si = make(map[string]*SaveItem, 256)
						bsi = createSaveItem()
						si[sc.board] = bsi
						m[u] = si
					}
					bsi.Count += item.Count
					bsi.Thread += item.Thread
					for id, idcount := range item.Id {
						bsi.Id[id] += idcount
					}
				}
			}
		}
	}(ch)
	return ch
}

func storeData(m map[int64]map[string]*SaveItem) {
	for key, bm := range m {
		writeFile(key, bm)
	}
}

func writeFile(k int64, bm map[string]*SaveItem) {
	date := time.Unix(k, 0).UTC()
	path := createPath(date)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0766)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	json.NewEncoder(w).Encode(bm)
	w.Flush()
	f.Close()
}

func createPath(t time.Time) string {
	return COUNT_PATH + "/" + t.Format("2006_01_02") + ".json"
}

func createSaveItem() *SaveItem {
	return &SaveItem{
		Id: make(map[string]int, 16),
	}
}
