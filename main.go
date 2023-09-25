package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mmcdole/gofeed"
)

type Config struct {
	Values         []string `json:"values"`
	ReFresh        int      `json:"refresh"`
	AutoUpdatePush int      `json:"autoUpdatePush"`
}

var (
	dbMap    map[string]feed
	rssUrls  Config
	upgrader = websocket.Upgrader{}
	lock     sync.RWMutex

	//go:embed static
	dirStatic embed.FS
	//go:embed index.html
	fileIndex embed.FS

	htmlContent []byte
)

func init() {
	// 读取配置文件
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		panic(err)
	}
	// 解析JSON数据到Config结构体
	err = json.Unmarshal(data, &rssUrls)
	if err != nil {
		panic(err)
	}
	// 读取 index.html 内容
	htmlContent, err = fileIndex.ReadFile("index.html")
	if err != nil {
		panic(err)
	}

	dbMap = make(map[string]feed)
}

func main() {
	go updateFeeds()
	http.HandleFunc("/feeds", getFeedsHandler)
	http.HandleFunc("/ws", wsHandler)
	// http.HandleFunc("/", serveHome)
	http.HandleFunc("/", tplHandler)

	//加载静态文件
	fs := http.FileServer(http.FS(dirStatic))
	http.Handle("/static/", fs)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/html; charset=utf-8")
	w.Write(htmlContent)
}

func tplHandler(w http.ResponseWriter, r *http.Request) {
	// 创建一个新的模板，并设置自定义分隔符为<< >>，避免与Vue的语法冲突
	tmplInstance := template.New("index.html").Delims("<<", ">>")
	//添加加法函数计数
	funcMap := template.FuncMap{
		"inc": func(i int) int {
			return i + 1
		},
	}
	// 加载模板文件
	tmpl, err := tmplInstance.Funcs(funcMap).ParseFS(fileIndex, "index.html")
	if err != nil {
		log.Println("模板加载错误:", err)
		return
	}

	// 定义一个数据对象
	data := struct {
		Keywords    string
		RssDataList []feed
	}{
		Keywords:    getKeywords(),
		RssDataList: getFeeds(),
	}

	// 渲染模板并将结果写入响应
	err = tmpl.Execute(w, data)
	if err != nil {
		log.Println("模板渲染错误:", err)
	}
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade failed: %v", err)
		return
	}

	defer conn.Close()
	for {
		for _, url := range rssUrls.Values {
			lock.RLock()
			cache, ok := dbMap[url]
			lock.RUnlock()
			if !ok {
				log.Printf("Error getting feed from db is null %v", url)
				continue
			}
			data, err := json.Marshal(cache)
			if err != nil {
				log.Printf("json marshal failure: %s", err.Error())
				continue
			}

			err = conn.WriteMessage(websocket.TextMessage, data)
			//错误直接关闭更新
			if err != nil {
				log.Printf("Error sending message or Connection closed: %v", err)
				return
			}
		}
		//如果未配置则不自动更新
		if rssUrls.AutoUpdatePush == 0 {
			return
		}
		time.Sleep(time.Duration(rssUrls.AutoUpdatePush) * time.Minute)
	}
}

func updateFeeds() {
	var (
		tick = time.Tick(time.Duration(rssUrls.ReFresh) * time.Minute)
		fp   = gofeed.NewParser()
	)
	for {
		formattedTime := time.Now().Format("2006-01-02 15:04:05")
		for _, url := range rssUrls.Values {
			go updateFeed(fp, url, formattedTime)
		}
		<-tick
	}
}

func updateFeed(fp *gofeed.Parser, url, formattedTime string) {
	fp.UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36 Edg/117.0.2045.40"
	result, err := fp.ParseURL(url)
	if err != nil {
		log.Printf("Error fetching feed: %v | %v", url, err)
		return
	}
	//feed内容无更新时无需更新缓存
	if cache, ok := dbMap[url]; ok &&
		len(result.Items) > 0 &&
		len(cache.Items) > 0 &&
		result.Items[0].Link == cache.Items[0].Link {
		return
	}
	customFeed := feed{
		Title:  result.Title,
		Link:   result.Link,
		Custom: map[string]string{"lastupdate": formattedTime},
		Items:  make([]item, 0, len(result.Items)),
	}
	for _, v := range result.Items {
		customFeed.Items = append(customFeed.Items, item{
			Link:        v.Link,
			Title:       v.Title,
			Description: v.Description,
		})
	}
	lock.Lock()
	defer lock.Unlock()
	dbMap[url] = customFeed
}

// 获取feeds列表
func getFeeds() []feed {
	feeds := make([]feed, 0, len(rssUrls.Values))
	for _, url := range rssUrls.Values {
		lock.RLock()
		cache, ok := dbMap[url]
		lock.RUnlock()
		if !ok {
			log.Printf("Error getting feed from db is null %v", url)
			continue
		}

		feeds = append(feeds, cache)
	}
	return feeds
}

// 获取关键词也就是title
// 获取feeds列表
func getKeywords() string {
	words := ""
	for _, url := range rssUrls.Values {
		lock.RLock()
		cache, ok := dbMap[url]
		lock.RUnlock()
		if !ok {
			log.Printf("Error getting feed from db is null %v", url)
			continue
		}
		if cache.Title != "" {
			words += cache.Title + ","
		}
	}
	return words
}

func getFeedsHandler(w http.ResponseWriter, r *http.Request) {
	feeds := getFeeds()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(feeds)
}
