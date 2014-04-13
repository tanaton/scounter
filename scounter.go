package main

import (
	"./get2ch"
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

const GO_THREAD_SLEEP_TIME = 2 * time.Second
const GO_BOARD_SLEEP_TIME = 5 * time.Second
const DAY5 = time.Hour * 24 * 5
const DAY7 = time.Hour * 24 * 7
const ROOT_PATH = "/2ch_sc/dat"
const OUTPUT_PATH = "/2ch_sc/scount"

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
	var sl, nsl map[string][]Nich
	var key string
	// get2ch開始
	get2ch.Start(g_cache, nil)
	sync := make(chan string)
	// メイン処理
	sl = getServer()
	for key = range sl {
		if h, ok := sl[key]; ok {
			go mainThread(key, h, sync)
			time.Sleep(GO_THREAD_SLEEP_TIME)
		}
	}
	for {
		// 処理を止める
		key = <-sync
		nsl = getServer()
		for k := range nsl {
			if _, ok := sl[k]; !ok {
				go mainThread(k, nsl[k], sync)
				time.Sleep(GO_THREAD_SLEEP_TIME)
			}
		}
		if h, ok := nsl[key]; ok {
			go mainThread(key, h, sync)
			time.Sleep(GO_THREAD_SLEEP_TIME)
		}
		sl = nsl
	}
}

func mainThread(key string, bl []Nich, sync chan string) {
	for _, nich := range bl {
		// 板の取得
		tl := getBoard(nich)
		if tl != nil && len(tl) > 0 {
			// スレッドの取得
			getThread(tl, nich.board)
		}
		// 少し待機
		time.Sleep(GO_BOARD_SLEEP_TIME)
	}
	sync <- key
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

func getThread(tl []Nich, board string) {
	sc := &ScCountPacket{
		board: board,
		count: make(map[int64]int, 5),
	}
	for _, nich := range tl {
		old, err := g_cache.GetData(nich.server, nich.board, nich.thread)
		get := get2ch.NewGet2ch(nich.board, nich.thread)
		data, err := get.GetData()
		if err != nil {
			gLogger.Printf(err.Error() + "\n")
			gLogger.Printf("%s/%s/%s\n", nich.server, nich.board, nich.thread)
		} else if get.GetHttpCode()/100 == 2 {
			// カウント処理
			scCount(data[len(old):], sc)
			gLogger.Printf("%d OK %s/%s/%s\n", get.GetHttpCode(), nich.server, nich.board, nich.thread)
		}
	}
	if len(sc.count) > 0 {
		// 集計
		gScCountCh <- sc
	}
}

func scCount(data []byte, sc *ScCountPacket) {
	before := time.Now().Add(DAY5 * -1).UTC()
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
		m := make(map[int64]map[string]int, 10)
		for {
			select {
			case <-wc:
				// 5分毎にファイルに書き出し
				for key, bm := range m {
					writeFile(key, bm)
				}
			case now := <-delc:
				// 7日以上前のデータを削除
				unix := now.Add(DAY7 * -1).UTC().Unix()
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

func writeFile(k int64, bm map[string]int) {
	date := time.Unix(k, 0).UTC()
	path := OUTPUT_PATH + "/" + date.Format("2006_01_02") + ".txt"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
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
