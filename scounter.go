package main

import (
	"./get2ch"
	"./kill"
	"bufio"
	"bytes"
	"fmt"
	"log"
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
	count map[int64]int
}

type KeyPacket struct {
	key    string
	killch chan struct{}
}

const (
	GO_THREAD_SLEEP_TIME = 2 * time.Second
	GO_BOARD_SLEEP_TIME  = 5 * time.Second
	DAY                  = time.Hour * 24
	ROOT_PATH            = "/2ch_sc/dat"
	COUNT_PATH           = "/2ch_sc/scount"
)

var g_reg_bbs = regexp.MustCompile(`(.+\.2ch\.sc)/(.+)<>`)
var g_reg_dat = regexp.MustCompile(`^(.+)\.dat<>`)
var g_reg_res = regexp.MustCompile(` \(([0-9]+)\)$`)
var g_reg_date = regexp.MustCompile(`(\d{1,4})\/(\d{1,2})\/(\d{1,2})`)
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
	// get2ch開始
	get2ch.Start(g_cache, nil)
	// 今までのキャッシュを読み込み
	loadRes()
	notice := make(chan KeyPacket)
	sl := getServer()
	nsl := sl
	// メイン処理
	startCrawler(nsl, notice)

	tick := time.Tick(time.Minute * 10)
	for {
		select {
		case <-tick:
			// 10分毎に板一覧を更新
			nsl = getServer()
		case it := <-notice:
			// どこかの鯖のクロールが終わった
			var flag bool
			if len(sl) != len(nsl) {
				// 鯖が増減した
				flag = true
			} else if _, ok := nsl[it.key]; !ok {
				// 鯖が消えた
				flag = true
			}

			if flag {
				// 今のクローラーを殺す
				close(it.killch)
				// 鯖を更新
				sl = nsl
				// 新クローラーの立ち上げ
				startCrawler(nsl, notice)
			} else if checkOpen(it.killch) {
				// クロール復帰
				go mainThread(it.key, nsl[it.key], notice, it.killch)
			}
		}
	}
}

func startCrawler(sl map[string][]Nich, notice chan<- KeyPacket) {
	killch := make(chan struct{})
	for key, it := range sl {
		go mainThread(key, it, notice, killch)
		time.Sleep(GO_THREAD_SLEEP_TIME)
	}
}

func loadRes() {
	now := time.Now() // UTCにしない
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
	scanner := bufio.NewScanner(fp)
	for scanner.Scan() {
		list := strings.Split(scanner.Text(), "\t")
		if len(list) <= 1 {
			continue
		}
		res, err := strconv.Atoi(list[1])
		if err != nil {
			continue
		}
		sc := &ScCountPacket{
			board: list[0],
			count: make(map[int64]int, 1),
		}
		date := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		sc.count[date.Unix()] = res
		// ロードする
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

func mainThread(key string, bl []Nich, notice chan<- KeyPacket, killch chan struct{}) {
	for _, nich := range bl {
		// 板の取得
		tl := getBoard(nich)
		if tl != nil && len(tl) > 0 {
			// スレッドの取得
			getThread(tl, nich.board, killch)
		}
		if checkOpen(killch) == false {
			// 緊急停止
			break
		}
		// 少し待機
		time.Sleep(GO_BOARD_SLEEP_TIME)
	}
	time.Sleep(GO_THREAD_SLEEP_TIME)
	notice <- KeyPacket{
		key:    key,
		killch: killch,
	}
}

func getServer() map[string][]Nich {
	var nich Nich
	get := get2ch.NewGet2ch("", "")
	sl := make(map[string][]Nich)
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
			if h, ok := sl[nich.server]; ok {
				sl[nich.server] = append(h, nich)
			} else {
				sl[nich.server] = []Nich{nich}
			}
		}
	}
	return sl
}

func getBoard(nich Nich) []Nich {
	get := get2ch.NewGet2ch(nich.board, "")
	h := threadResList(nich)
	data, err := get.GetData()
	if err != nil {
		gLogger.Printf(err.Error() + "\n")
		return nil
	}
	get.GetBoardName()
	vect := make([]Nich, 0, 128)
	var n Nich
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
	return vect
}

func threadResList(nich Nich) map[string]int {
	data, err := g_cache.GetData(nich.server, nich.board, "")
	h := make(map[string]int)
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
		count: make(map[int64]int, 5),
	}
	for _, nich := range tl {
		old, err := g_cache.GetData(nich.server, nich.board, nich.thread)
		if err != nil {
			old = []byte{}
		}
		get := get2ch.NewGet2ch(nich.board, nich.thread)
		data, err := get.GetData()
		if err != nil {
			gLogger.Println(err)
			gLogger.Printf("%s/%s/%s\n", nich.server, nich.board, nich.thread)
		} else if (get.GetHttpCode()/100) == 2 && len(data) > len(old) {
			// カウント処理
			scCount(data[len(old):], sc)
			gLogger.Printf("%d OK %s/%s/%s\n", get.GetHttpCode(), nich.server, nich.board, nich.thread)
		}
		if checkOpen(killch) == false {
			// 緊急停止
			break
		}
	}
	if len(sc.count) > 0 {
		// 集計
		gScCountCh <- sc
	}
}

func scCount(data []byte, sc *ScCountPacket) {
	before := time.Now().Add(DAY * 3 * -1).UTC()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
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
				sc.count[t.Unix()] += 1
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
		wc := time.Tick(time.Minute * 5)
		delc := time.Tick(time.Hour * 24)
		killc := kill.CreateKillChan()
		m := make(map[int64]map[string]int, 5)
		for {
			select {
			case <-killc:
				// 終了時にファイルに書き出し
				storeData(m)
				// 強制終了
				os.Exit(1)
			case <-wc:
				// 5分毎にファイルに書き出し
				storeData(m)
			case now := <-delc:
				// 3日以上前のデータを削除
				unix := now.Add(DAY * 3 * -1).UTC().Unix()
				dl := []int64{}
				for key, _ := range m {
					if key < unix {
						dl = append(dl, key)
					}
				}
				for _, it := range dl {
					delete(m, it)
				}
			case it := <-ch:
				// データの追加
				for key, num := range it.count {
					bm, ok := m[key]
					if ok {
						bm[it.board] += num
					} else {
						bm = make(map[string]int, 1024)
						bm[it.board] = num
						m[key] = bm
					}
				}
			}
		}
	}(ch)
	return ch
}

func storeData(m map[int64]map[string]int) {
	for key, bm := range m {
		writeFile(key, bm)
	}
}

func writeFile(k int64, bm map[string]int) {
	date := time.Unix(k, 0).UTC()
	path := createPath(date)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0766)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for board, num := range bm {
		fmt.Fprintf(w, "%s\t%d\n", board, num)
	}
	w.Flush()
}

func createPath(t time.Time) string {
	return COUNT_PATH + "/" + t.Format("2006_01_02") + ".txt"
}
