package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gocolly/colly"
	"golang.org/x/net/proxy"
)

// //go:embed resource.res
// var resourceData string
// var defaultBuildConnTimeout = 5 * time.Second

const maxToleranceTimesForNetwork = 30

const defualtRequestTimeout = 10 * time.Hour

const defaultSocksRecoverTime = 5 * time.Second

const smoothRequest = 15 * time.Millisecond

var trembleOffsetTime = 1.3

// default conns number for concurrent
const defaultConnTimesForConcurrent = 5

// request timeout for global
const defaultRequestTimeout = 2 * time.Minute

// ignore error ,which be resolved by

var ignoreErrorMap = map[error]any{
	io.ErrUnexpectedEOF:      nil,
	io.EOF:                   nil,
	net.ErrClosed:            nil,
	colly.ErrAlreadyVisited:  nil,
	context.DeadlineExceeded: nil,
}

// var originPackge []byte

// var singleTemplate *requestTemplate = new(requestTemplate)

// var c *colly.Collector

// channel for block
// var finalizeSignal = make(chan error, 0)

// type forceError struct {
// 	errMsg string
// }

// func (force *forceError) Error() string {
// 	return "force error"
// }

// the combined by package tempalte
// var unlessRequest *http.Request

// var taskSignalMap = make(map[string]string)

type controller struct {
	// the origin request for package
	unlessRequest *http.Request
	// the singal order to finalize the task for collector
	finalizeSignal chan error
	// the origin package for requeust
	originPackage  []byte
	singleTemplate *requestTemplate
	// the  request queue for managing all request
	c *colly.Collector
	// // initing the request for  collector
	handleChain repliteChain
	// the variable params for persistenting data
	taskSignalMap map[string]*VariableParams
	mapRWMutex    sync.RWMutex
	// controller the request sending.
	// 0 represent normal, 1 represent the temporary error ,2 represent the panic error
	requestMutex int32
	// //proxy status
	// proxyStatus bool
	// auto defined params for user
	params *CmdParams
	// cookie change status
	cookiesChange bool
	// current cookies if cant change the cookie is empty
	cookies        string
	metrics        *Metrics
	opTolerance    int32
	contentTremble *Tremble
	finish         bool
	cookieChange   int32
	stdMutex       *sync.RWMutex
	failedChan     *sync.RWMutex
	// once           *sync.Once
}

type Tremble struct {
	// default the first response must be true
	Base               int
	AppearTime         int32 `json:"-"`
	MinToleranceLength int
	CurTrembleValue    int `json:"-"`
}

type VariableParams struct {
	Id    int32
	Value string
}

type CmdParams struct {
	FileStr          string
	From             int
	To               int
	Step             int
	ConcurrentNumber int
	IntervalTime     time.Duration
	DictData         string
	Username         string
	Password         string
	Ip               string
	Port             int
	List             string
	IsList           bool
	IsPage           bool
	Fixup            string
	TargetDictLoc    string
	HasProxy         bool
}

type Metrics struct {
	NowStartTime   time.Time `json:"-"`
	ConsistentTime time.Duration
}

type ProxyServer struct {
	Ip       string
	Port     int
	Username string
	Password string
}

type adapterOptions func(*controller)

func withLimitRule(limit *colly.LimitRule) adapterOptions {
	return func(repliteController *controller) {
		repliteController.c.Limit(limit)
	}
}
func withRequestTimeout(time time.Duration) adapterOptions {
	return func(repliteController *controller) {
		repliteController.c.SetRequestTimeout(time)
	}
}

func (repliteController *controller) buildCollector(proxyServer *ProxyServer, options ...adapterOptions) {
	repliteController.c = colly.NewCollector()
	repliteController.c.SetRequestTimeout(defaultRequestTimeout)
	// c.Async = true
	if repliteController.params.HasProxy {
		socks5, err := proxy.SOCKS5("tcp", fmt.Sprintf("%s:%d", proxyServer.Ip, proxyServer.Port), &proxy.Auth{User: proxyServer.Username, Password: proxyServer.Password}, proxy.Direct)
		if err != nil {
			panic(fmt.Sprintf("sock5代理服务器连接错误:%s", err.Error()))
		}
		// transports := http.DefaultTransport

		transport := http.Transport{Dial: socks5.Dial, MaxIdleConns: repliteController.params.ConcurrentNumber * defaultConnTimesForConcurrent, IdleConnTimeout: 90 * time.Second, DisableKeepAlives: false}
		repliteController.c.WithTransport(&transport)
	}
	for i := 0; i < len(options); i++ {
		options[i](repliteController)
	}
	repliteController.c.OnError(func(r *colly.Response, err error) {
		if atomic.LoadInt32(&repliteController.opTolerance) > maxToleranceTimesForNetwork {
			repliteController.finalizeSignal <- fmt.Errorf("因网络原因导致重新建立连接多次,建议检查后台是否在线和所设线程的数量,防止被后台检测到和网络拥塞造成代理服务器崩溃:%s", err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			r.Request.Retry()
			return
		}
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) {
			repliteController.retryConn()
			r.Request.Retry()
			return
		}
		if strings.Contains(err.Error(), "the connected party did not properly respond after a period of time") || strings.Contains(err.Error(), " connected host has failed to respond") {
			repliteController.retryConn()
			r.Request.Retry()
			return
		}
	})
	repliteController.c.OnRequest(func(r *colly.Request) {
		for atomic.LoadInt32(&repliteController.requestMutex) != 0 {
			runtime.Gosched()
		}
		if repliteController.cookiesChange {
			r.Headers.Del("Cookie")
			r.Headers.Set("Cookie", repliteController.cookies)
		}
	})

	repliteController.c.OnResponse(func(r *colly.Response) {
		if atomic.LoadInt32(&repliteController.requestMutex) == 1 || r.StatusCode != 200 {
			r.Request.Retry()
			return
		}
		if atomic.LoadInt32(&repliteController.opTolerance) > maxToleranceTimesForNetwork {
			repliteController.finalizeSignal <- errors.New("因网络原因导致重新建立连接多次,建议检查后台是否在线和所设线程的数量,防止被后台检测到和网络拥塞造成代理服务器崩溃")
			return
		}
		// if r.StatusCode != 200 {
		// maybe is proxy error
		// if repliteController.proxyStatus {
		// 	proxyRecover(repliteController, r)
		// } else {

		// repliteController.finalizeSignal <- errors.New("后台服务器掉了!!!!!!!!!!!!!!!!")
		// }
		// 	r.Request.Retry()
		// 	return
		// }

		// for !repliteController.proxyStatus {
		// 	parseRequest.Wait()
		// }
		id := r.Request.Ctx.Get("id")

		//TODO tremable inspect
		//TODO if the cookie be recoverd need to execute ,but the proxy conn acquire the mutex  cause the cookieRecover cant execute
		if repliteController.checkTremble(len(r.Body)) {
			//release the CAS requestMutex  pressure,and upgrade the mutex granularity
			if atomic.CompareAndSwapInt32(&repliteController.cookieChange, 0, 1) {
				//the cookie recover operate should be consider as the first task of executing,so the requestMutex will be CAS  util the cookieRecover obtains the mutex to recover
				repliteController.cookieRecover(r)
				// repliteController.stdMutex.Lock()
				// fmt.Printf("当前response size变动较大,可能是cookie过期,请检查%s文件\n", id)
				// fmt.Println("是否要退出程序:(true or false)")
				// var exitFlag string
				// fmt.Scanln(&exitFlag)
				// if strings.ToLower(exitFlag) == "true" {
				// 	repliteController.finalizeSignal <- errors.New("自愿退出程序")
				// }
				// fmt.Println("是否需要改变cookie:(true or false):")
				// var flag string
				// fmt.Scanln(&flag)
				// repliteController.stdMutex.Unlock()
				// if strings.ToLower(flag) == "true" {
				// 	repliteController.cookieRecover(r)
				// 	r.Request.Retry()
				// 	return
				// } else {
				// 	//update the tremble factor
				// 	repliteController.contentTremble.MinToleranceLength = repliteController.contentTremble.CurTrembleValue
				// }
				atomic.CompareAndSwapInt32(&repliteController.cookieChange, 1, 0)
			} else {
				time.Sleep(smoothRequest)
			}
			// r.Request.Retry()
			//recover tremble records
			atomic.StoreInt32(&repliteController.contentTremble.AppearTime, 0)
		}
		//TODO maybe  concurrent error for l1-l3 cache
		// repliteController.proxyStatus = true
		err := r.Save(fmt.Sprintf("%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, id))
		if err != nil {
			repliteController.finalizeSignal <- fmt.Errorf("持久化文件出错%s", err.Error())
			return
		}
		repliteController.mapRWMutex.Lock()
		defer repliteController.mapRWMutex.Unlock()
		repliteController.stdMutex.RLock()
		defer repliteController.stdMutex.RUnlock()
		fmt.Printf("成功爬取:%s\n", repliteController.taskSignalMap[id].Value)
		// delete the success id
		delete(repliteController.taskSignalMap, id)
	})
}

func (repliteController *controller) retryConn() {
	// In current high concurrent system,the retryConn operate should be consider as the frequently,comparing as the cookieRecover, so  'if CAS' operate  has same as effect for cookieRecover 'for CAS'
	// but It is worth noting that  the cookieRecover priority should higher than the retryConn in actual situation, that's also why the author designed it this way
	if !atomic.CompareAndSwapInt32(&repliteController.requestMutex, 0, 1) {
		return
	}
	fmt.Println("正在重连代理服务器")
	// wait the async task maybe pass CAS is false to exit the retryConn
	time.Sleep(defaultSocksRecoverTime)
	if repliteController.params.HasProxy {
		renewSocks5, err := proxy.SOCKS5("tcp", fmt.Sprintf("%s:%d", repliteController.params.Ip, repliteController.params.Port), &proxy.Auth{User: repliteController.params.Username, Password: repliteController.params.Password}, proxy.Direct)
		if err != nil {
			repliteController.finalizeSignal <- fmt.Errorf("socks5代理服务器连接错误:%s", err.Error())
		}
		transport := http.Transport{Dial: renewSocks5.Dial, MaxIdleConns: repliteController.params.ConcurrentNumber * defaultConnTimesForConcurrent, IdleConnTimeout: 90 * time.Second, DisableKeepAlives: false}
		repliteController.c.WithTransport(&transport)
	}
	//TODO if hasnt the proxy,how to repair this question
	atomic.AddInt32(&repliteController.opTolerance, 1)
	atomic.CompareAndSwapInt32(&repliteController.requestMutex, 1, 0)
}

func (repliteController *controller) checkTremble(size int) bool {
	if size >= repliteController.contentTremble.MinToleranceLength {
		return false
	}
	repliteController.contentTremble.CurTrembleValue = size
	atomic.AddInt32(&repliteController.contentTremble.AppearTime, 1)
	if atomic.LoadInt32(&repliteController.contentTremble.AppearTime) < 3 {
		return false
	}
	return true
}
func (repliteController *controller) cookieRecover(r *colly.Response) {
	atomic.AddInt32(&repliteController.opTolerance, 1)
	for atomic.CompareAndSwapInt32(&repliteController.requestMutex, 0, 1) {
		break
	}
	defer atomic.CompareAndSwapInt32(&repliteController.requestMutex, 1, 0)
	repliteController.stdMutex.Lock()
	defer repliteController.stdMutex.Unlock()
	id := r.Request.Ctx.Get("id")
	fmt.Printf("当前response size变动较大,可能是cookie过期,请检查%s文件\n", id)
	nextSingal := make(chan any, 0)
	go func(nextChan chan any) {
		once := sync.Once{}
		timer := time.NewTimer(5 * time.Minute)
		var hasInput bool
		for {
			select {
			case <-timer.C:
				if !hasInput {
					repliteController.finalizeSignal <- errors.New("自愿退出程序")
					return
				}
			default:
				once.Do(func() {
					fmt.Println("是否要退出程序:(true or false)")
					var exitFlag string = "true"
					fmt.Scanln(&exitFlag)
					hasInput = true
					if strings.ToLower(exitFlag) == "true" {
						repliteController.finalizeSignal <- errors.New("自愿退出程序")
					} else {
						//can pass
						nextChan <- struct{}{}
					}
				})
			}
		}
	}(nextSingal)
	<-nextSingal
	fmt.Println("是否需要改变cookie:(true or false):")
	var flag string
	fmt.Scanln(&flag)
	if strings.ToLower(flag) == "true" {
		r.Request.Retry()
	} else {
		//update the tremble factor
		repliteController.contentTremble.MinToleranceLength = repliteController.contentTremble.CurTrembleValue
		return
	}
	// give a chance to require
	var newCookies = make([]*http.Cookie, 0, len(repliteController.unlessRequest.Cookies()))
	curCookie := r.Request.Headers.Get("Cookie")
	fmt.Printf("当前的Cookie为:%s\n", curCookie)
	for {
		var cookie string
		fmt.Printf("请重新输入Cookie:\n")
		fmt.Scanf("%s", &cookie)
		vals := strings.Split(cookie, "=")
		if len(vals) != 2 {
			fmt.Printf("输入的cookie不符合规范(应该为key=value类型):%s\n", cookie)
			continue
		}
		newCookies = append(newCookies, &http.Cookie{Name: vals[0], Value: vals[1]})
		fmt.Println("当前Cookie为:")
		for i := 0; i < len(newCookies); i++ {
			fmt.Println(newCookies[i].String())
		}
		var continueFlag string
		fmt.Printf("\n是否继续输入cookie:(true or false):\n")
		fmt.Scanf("%s", continueFlag)
		if strings.ToLower(continueFlag) != "true" {
			break
		}
	}
	repliteController.cookiesChange = true
	repliteController.cookies = cookiesToString(newCookies)
	// repliteController.proxyStatus = false
}
func cookiesToString(cookies []*http.Cookie) string {
	cookieStrs := make([]string, len(cookies))
	for i, cookie := range cookies {
		cookieStrs[i] = cookie.String()
	}
	return strings.Join(cookieStrs, "; ")
}

func (repliteController *controller) recoverPersistence() {
	// create find dictionary
	var targetDict = fmt.Sprintf("%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite")
	defer func() {
		if panicError := recover(); panicError != nil {
			fmt.Println(panicError)
			// os.RemoveAll(targetDict)
		}
	}()
	// var pathError *os.PathError
	if _, err := os.Open(targetDict); os.IsNotExist(err) {
		os.Mkdir(targetDict, 0644)
	}
	// keep the origin package to next user
	file, err := os.OpenFile(fmt.Sprintf("%s%c%s", targetDict, os.PathSeparator, "originPackage.txt"), os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(fmt.Sprintf("持久化未爬取的数据时发生未知错误:%s", err.Error()))
	}
	io.Copy(file, bytes.NewBuffer(repliteController.originPackage))
	// keep the uncomplete variable params to file
	repliteController.mapRWMutex.Lock()
	defer repliteController.mapRWMutex.Unlock()
	unfinishFile, err := os.OpenFile(fmt.Sprintf("%s%c%s", targetDict, os.PathSeparator, "unfinish.json"), os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(fmt.Sprintf("持久化未爬取的数据时发生未知错误:%s", err.Error()))
	}
	uncompleteBys, err := json.Marshal(repliteController.taskSignalMap)
	if err != nil {
		panic(fmt.Sprintf("持久化未爬取的数据时发生未知错误:%s", err.Error()))
	}
	io.Copy(unfinishFile, bytes.NewBuffer(uncompleteBys))
	paramsInfoBys, err := json.Marshal(repliteController.params)
	if err != nil {
		panic(fmt.Sprintf("持久化用户参数信息时发生未知错误:%s", err.Error()))
	}
	paramsFile, err := os.OpenFile(fmt.Sprintf("%s%c%s", targetDict, os.PathSeparator, "params.json"), os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(fmt.Sprintf("持久化未爬取的数据时发生未知错误:%s", err.Error()))
	}
	io.Copy(paramsFile, bytes.NewBuffer(paramsInfoBys))
	fmt.Println("-------------------------------------------未爬取的数据备份成功,可使用--fixup参数修复------------------------------------------------------------------------")
}

func (repliteController *controller) saveMetrics() {
	endTime := time.Now()
	repliteController.metrics.ConsistentTime += endTime.Sub(repliteController.metrics.NowStartTime)
	metricsBys, err := json.Marshal(repliteController.metrics)
	if err != nil {
		panic(fmt.Sprintf("持久化metrics信息时发生未知错误:%s", err.Error()))
	}
	metricsFile, err := os.OpenFile(fmt.Sprintf("%s%c%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite", os.PathSeparator, "metrics.json"), os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(fmt.Sprintf("持久化未爬取的数据时发生未知错误:%s", err.Error()))
	}
	io.Copy(metricsFile, bytes.NewBuffer(metricsBys))
}

func main() {
	//create and execute the replite chain,and create the parasmCollections
	controller := newController(handleParams())
	// before execute the chain link ,start the monitor
	// create .replite hidden dict
	dict := fmt.Sprintf("%s%c%s", controller.params.TargetDictLoc, os.PathSeparator, ".replite")
	// var pathError *os.PathError
	if _, err := os.Open(dict); os.IsNotExist(err) {
		os.Mkdir(dict, 0644)
	}
	// start metrics
	controller.startingMetrics()
	// defer func() {
	// 	controller.once.Do(func() {
	defer controller.saveMetrics()
	//operate signal
	exitChan := make(chan os.Signal, 0)
	// system is forced exit
	signal.Notify(exitChan, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for {
			select {
			case err := <-controller.finalizeSignal:
				// change the flag to stop all request
				atomic.StoreInt32(&controller.requestMutex, 2)
				fmt.Println("正在退出", err)
				//save metrics when final error occur
				// controller.once.Do(func() {
				controller.saveMetrics()
				// 保存未能爬取的数据集合
				controller.recoverPersistence()
				// })
				os.Exit(1)
				// receive the operation system signal to finalizer
			case <-exitChan:
				atomic.StoreInt32(&controller.requestMutex, 2)
				controller.saveMetrics()
				// 保存未能爬取的数据集合
				controller.recoverPersistence()
				os.Exit(0)
			}
		}
	}()
	// 保存未能爬取的数据集合
	// controller.recoverPersistence()
	// })
	// }()

	//TODO start performance reporter
	func() {
		file, err := os.OpenFile(fmt.Sprintf("%s%c%s", dict, os.PathSeparator, "trace.out"), os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			panic(fmt.Sprintf("生成检测报告文件失败:%s", err.Error()))
		}
		err = trace.Start(file)
		if err != nil {
			panic(fmt.Sprintf("开启检测检测失败:%s", err.Error()))
		}
	}()
	defer trace.Stop()
	controller.requestProbe()
	// create the request and  send to colly.collector to execute
	controller.executeRepliteChain()
	// force enter wait
	// controller.c.Wait()
	controller.finish = true
	controller.finalizer()
	controller.releaseUnless()
	fmt.Println("\n-----------------本次任务执行完毕--------------")
	// c.Async = true
}

/*
*

	analysis the error file and missing file, and retry get it

*
*/
func (repliteController *controller) finalizer() {
	fmt.Println("---------正在扫描全部response是否符合预期------")
	fileHeap := make(PriorityQueue, 0)
	var allMap = make(map[string]*VariableParams)
	var ExistsInt = make(map[int64]any, len(allMap))
	allParamsfileStr := fmt.Sprintf("%s%c%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite", os.PathSeparator, "allTaskVar.json")
	file, err := os.Open(allParamsfileStr)
	if err != nil {
		panic(fmt.Sprintf("打开持久化的所有可变参数文件%s失败:%s", allParamsfileStr, err.Error()))
	}
	json.NewDecoder(file).Decode(&allMap)
	var renewParams = make(map[string]*VariableParams)
	filepath.Walk(repliteController.params.TargetDictLoc, func(path string, info fs.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		var fileInt int64
		if fileInt, err = strconv.ParseInt(info.Name(), 10, 64); err != nil {
			fmt.Printf("忽略文件%s\n", info.Name())
			return nil
		}
		cur := &FItem{
			priority: int(info.Size()),
			value:    info.Name(),
		}
		ExistsInt[fileInt] = nil
		fileHeap = append(fileHeap, cur)
		return nil
	})
	//TODO struct add the field to ignore this circle
	var begin int64 = math.MaxInt64
	var end int64 = math.MinInt64
	for key, _ := range allMap {
		keyInt, _ := strconv.ParseInt(key, 10, 64)
		if keyInt < begin {
			begin = keyInt
		}
		if keyInt > end {
			end = keyInt
		}
	}
	for i := int(begin); i <= int(end); i++ {
		if _, ok := ExistsInt[int64(i)]; !ok {
			renewParams[strconv.FormatInt(int64(i), 10)] = allMap[strconv.FormatInt(int64(i), 10)]
		}
	}
	fmt.Println("检测到未爬取的文件有:")
	printRenewFiles(&renewParams)
	sort.Sort(fileHeap)
	var isRecover string = "false"
	fmt.Println("是否要进行错误文件扫描:(true or false)")
	fmt.Scanln(&isRecover)
	if strings.ToLower(isRecover) != "false" {
		var errorOffset = int(float64((fileHeap[0].priority + fileHeap[len(fileHeap)-1].priority)) / 2)
		fmt.Printf("继续判断错误的文件大小在%d以内,是否需要更改:(true or false)\n", errorOffset)
		var operate string
		fmt.Scanln(&operate)
		if strings.ToLower(operate) == "true" {
			fmt.Print("请输入实际错误文件的大小为0-")
			_, err = fmt.Scanln(&errorOffset)
			if err != nil {
				panic("请输入数字")
			}
		}
		var flag bool = true
		var cur = 0
		for flag {
			if cur >= len(fileHeap) {
				break
			}
			fInfo := fileHeap[cur]
			if fInfo == nil {
				break
			}
			// itemStr := fInfo.value
			// item, err := strconv.ParseInt(itemStr, 10, 64)
			// if err != nil {
			// 	panic(err.Error())
			// }
			if fInfo.priority <= errorOffset {
				renewParams[fInfo.value] = allMap[fInfo.value]
			} else {
				flag = false
			}
			cur++
		}
	}

	fmt.Println("检测到需要恢复的全部文件:")
	printRenewFiles(&renewParams)
	fmt.Println("总共需要修复的文件数量:", len(renewParams))
	repliteController.taskSignalMap = renewParams
	fmt.Println("----------正在恢复错误的文件-----------")
	if len(repliteController.taskSignalMap) > 0 {
		repliteController.handleChain.handle(repliteController)
	}
}

func printRenewFiles(params *map[string]*VariableParams) {
	var curNumber = 0
	for key, _ := range *params {
		fmt.Printf("%6s", key)
		curNumber++
		if curNumber >= 6 {
			fmt.Println()
			curNumber = 0
		}
	}
	fmt.Println()
}

// execute this method before all controller params is ready
func (repliteController *controller) requestProbe() {
	repliteController.contentTremble = new(Tremble)
	fileStr := fmt.Sprintf("%s%c%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite", os.PathSeparator, "tremble.json")
	if repliteController.params.Fixup != "" {
		file, err := os.Open(fileStr)
		if err != nil {
			panic(fmt.Sprintf("打开持久化文件%s出错:%s", fileStr, err.Error()))
		}
		err = json.NewDecoder(bufio.NewReader(file)).Decode(repliteController.contentTremble)
		if err != nil {
			panic(fmt.Sprintf("反序列化文件%s出错:%s", fileStr, err.Error()))
		}
	} else {
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(fmt.Sprintf("%s%s%s", string(repliteController.singleTemplate.prefix), string(repliteController.singleTemplate.originContent), string(repliteController.singleTemplate.suffix)))))
		if err != nil {
			panic(fmt.Sprintf("预生成request出错:%s", err.Error()))
		}
		req.RequestURI = ""
		// req, err := parseRawRequest(request)
		// if err != nil {
		// 	panic(fmt.Sprintf("转化为request对象出错:%s", err.Error()))
		// }
		//forerunner
		client := http.Client{}
		if repliteController.params.HasProxy {
			forerunner, err := proxy.SOCKS5("tcp", fmt.Sprintf("%s:%d", repliteController.params.Ip, repliteController.params.Port), &proxy.Auth{User: repliteController.params.Username, Password: repliteController.params.Password}, proxy.Direct)
			if err != nil {
				panic(fmt.Errorf("socks5代理服务器连接错误:%s", err.Error()))
			}
			client.Transport = &http.Transport{Dial: forerunner.Dial}
		}
		defer client.CloseIdleConnections()
		response, err := client.Do(req)
		if err != nil {
			panic(fmt.Sprintf("预发送请求%v失败:%s,请检查相关代理服务器或者后台以及网络连接是否正常", req, err.Error()))
		}
		var realBody io.Reader = response.Body
		if response.Header.Get("Content-Encoding") == "gzip" {
			realBody, err = gzip.NewReader(response.Body)
			if err != nil {
				panic(fmt.Sprintf("%v解压失败;%s", response, err.Error()))
			}
		}
		bys, err := io.ReadAll(realBody)
		if err != nil {
			panic(fmt.Sprintf("读取预请求%v的响应体失败:%s", req, err.Error()))
		}
		repliteController.contentTremble.Base = len(bys)
		repliteController.contentTremble.MinToleranceLength = int(float64(len(bys)) / trembleOffsetTime)
		// keep file to .replite dict
		file, err := os.OpenFile(fileStr, os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			panic(fmt.Sprintf("持久化文件%s出错%s", fileStr, err.Error()))
		}
		trembleByt, err := json.Marshal(repliteController.contentTremble)
		if err != nil {
			panic(fmt.Sprintf("序列化%v失败:%s", repliteController.contentTremble, err.Error()))
		}
		io.Copy(file, bytes.NewBuffer(trembleByt))
	}
}

// func parseRawRequest(request *http.Request) (req *http.Request, err error) {
// 	fmt.Println(req.RequestURI)
// 	parsedURL, err := url.Parse(req.RequestURI)
// 	if err != nil {
// 		return nil, err
// 	}
// 	request.URL = parsedURL
// 	return request, err
// }

// func setRequestBody(req *http.Request, body io.Reader) {
// 	if body != nil {
// 		switch v := body.(type) {
// 		case *bytes.Buffer:
// 			req.ContentLength = int64(v.Len())
// 			buf := v.Bytes()
// 			req.GetBody = func() (io.ReadCloser, error) {
// 				r := bytes.NewReader(buf)
// 				return io.NopCloser(r), nil
// 			}
// 		case *bytes.Reader:
// 			req.ContentLength = int64(v.Len())
// 			snapshot := *v
// 			req.GetBody = func() (io.ReadCloser, error) {
// 				r := snapshot
// 				return io.NopCloser(&r), nil
// 			}
// 		case *strings.Reader:
// 			req.ContentLength = int64(v.Len())
// 			snapshot := *v
// 			req.GetBody = func() (io.ReadCloser, error) {
// 				r := snapshot
// 				return io.NopCloser(&r), nil
// 			}
// 		}
// 		if req.GetBody != nil && req.ContentLength == 0 {
// 			req.Body = http.NoBody
// 			req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
// 		}
// 	}
// }

func (repliteController *controller) startingMetrics() {
	repliteController.metrics = new(Metrics)
	repliteController.metrics.NowStartTime = time.Now()
	// metrics file isn't necessary
	if repliteController.params.Fixup != "" {
		file, err := os.Open(fmt.Sprintf("%s%c%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite", os.PathSeparator, "metrics.json"))
		if err != nil {
			fmt.Printf("ERROR:metrics starting failed:%s\n", err.Error())
			return
		}
		err = json.NewDecoder(bufio.NewReader(file)).Decode(repliteController.metrics)
		if err != nil {
			panic(fmt.Sprintf("metrics json decode failed:%s", err.Error()))
		}
	}
}

// if the unfinish variable params is empty, delete the .replite file
func (repliteController *controller) releaseUnless() {
	var curDict = fmt.Sprintf("%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite")
	// if curDict == "" {
	// 	curDict = fmt.Sprintf("%s%c%s", controller.params.TargetDictLoc, os.PathSeparator, ".replite")
	// }
	// file, err := os.Open(fmt.Sprintf("%s%c%s", curDict, os.PathSeparator, "unfinish.json"))
	// if err != nil {
	// 	return
	// }
	// var variableParamsMap = make(map[string]*VariableParams)
	// err = json.NewDecoder(bufio.NewReader(file)).Decode(&variableParamsMap)
	// if err != nil {
	// 	return
	// }
	repliteController.mapRWMutex.RLock()
	defer repliteController.mapRWMutex.RUnlock()
	unfinishStr := fmt.Sprintf("%s%c%s", curDict, os.PathSeparator, "unfinish.json")
	if len(repliteController.taskSignalMap) == 0 {
		//only delete the unfinish file
		os.Remove(unfinishStr)
		endTime := time.Now()
		fmt.Printf("本次总共耗时:%s", (endTime.Sub(repliteController.metrics.NowStartTime) + repliteController.metrics.ConsistentTime).String())
	} else {
		// is panic , recover the unfinish file,passing the fixup function to fix
		file, err := os.OpenFile(unfinishStr, os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			panic(fmt.Sprintf("持久化unfinish.json文件出错:%s", err.Error()))
		}
		unfinishByt, err := json.Marshal(repliteController.taskSignalMap)
		if err != nil {
			panic(fmt.Sprintf("序列化taskSingalMap(%v)出错:%s", repliteController.taskSignalMap, err.Error()))
		}
		io.Copy(file, bytes.NewBuffer(unfinishByt))
	}
}

// create replitechain and execute
func (repliteController *controller) executeRepliteChain() {
	if repliteController.params.From != 0 && repliteController.params.To != 0 {
		repliteController.params.IsPage = true
	} else if repliteController.params.List != "" {
		repliteController.params.IsList = true
	} else {
		if repliteController.params.Fixup == "" {
			repliteController.finalizeSignal <- fmt.Errorf("请认真检查你的可变参数是否合理:[from:%d, to:%d, step:%d, list:%s]", repliteController.params.From, repliteController.params.To, repliteController.params.Step, repliteController.params.List)
		}
	}
	var chain = new(fixupChain)
	chain.setNext(new(listChain)).setNext(new(pageChain))
	repliteController.handleChain = chain
	chain.handle(repliteController)
}

func handleParams() (cmd *CmdParams) {
	cmd = new(CmdParams)
	var username string
	var password string
	var ip string
	var port int
	var dictData string
	var from int
	var to int
	var step int
	var fileStr string
	var concurrentNumber int
	var intervalTime time.Duration
	var list string
	var fixup string
	flag.StringVar(&username, "username", "", "代理服务器验证的username(必填)")
	flag.StringVar(&password, "password", "", "代理服务器验证的password(必填)")
	flag.StringVar(&ip, "ip", "43.154.180.200", "代理服务器的IP")
	flag.IntVar(&port, "port", -1, "代理服务器的端口号(必填)")
	flag.StringVar(&dictData, "target", "", "将请求到的数据保存到哪个文件夹里面(必填)")
	flag.IntVar(&from, "from", 0, "从多少页开始")
	flag.IntVar(&to, "to", 0, "从多少页结束")
	flag.IntVar(&step, "step", 1, "请输入步长,默认为1")
	flag.StringVar(&fileStr, "package", "", "数据包txt文件路径(必填)")
	flag.IntVar(&concurrentNumber, "concurrent", 1, "请求线程数")
	flag.DurationVar(&intervalTime, "interval", 0, "每次请求的间隔时间")
	flag.StringVar(&list, "list", "", "请求参数以文件的形式给出(仅支持txt格式)")
	flag.StringVar(&fixup, "fixup", "", "修复因对面服务器崩掉或代理服务器挂掉导致数据没爬完的目标文件夹目录")
	flag.Parse()
	var preParamsInspect = "请检查你的输入参数,如果不是选择[--fixup],[--target],[--package]是必须指定的参数,执行一次爬虫的时候，可变参数只能是[--from], [--to] 或者 [--list] 其中一个"
	if fixup == "" {
		if fileStr == "" || (((from == 0 && to == 0) || (list == "")) && ((from == 0 && to == 0) && (list == ""))) {
			panic(preParamsInspect)
		}
		if port != -1 {
			cmd.HasProxy = true
		}
		cmd.TargetDictLoc = dictData
	} else {
		if dictData != "" {
			panic("--fixup 和 --target不能同时存在")
		}
		// rebuild params
		var paramsFileLoc = fmt.Sprintf("%s%c%s%c%s", fixup, os.PathSeparator, ".replite", os.PathSeparator, "params.json")
		file, err := os.Open(paramsFileLoc)
		if err != nil {
			fmt.Printf("持久化的用户参数文件不存在,请在参数中指出")
		}
		var paramsInfo = new(CmdParams)
		err = json.NewDecoder(file).Decode(paramsInfo)
		if err != nil {
			panic(fmt.Sprintf("序列化用户参数文件(%s)出错:%s", paramsFileLoc, err.Error()))
		}
		cmd = paramsInfo
		cmd.Fixup = fixup
		cmd.TargetDictLoc = fixup
		return
	}
	// create the dict
	// var pathError *os.PathError
	if _, err := os.Open(dictData); os.IsNotExist(err) {
		os.Mkdir(dictData, 0644)
	}
	cmd.Username = username
	cmd.Password = password
	cmd.Ip = ip
	cmd.Port = port
	cmd.DictData = dictData
	cmd.From = from
	cmd.To = to
	cmd.Step = step
	cmd.FileStr = fileStr
	cmd.ConcurrentNumber = concurrentNumber
	cmd.IntervalTime = intervalTime
	cmd.List = list
	var targetDict = fmt.Sprintf("%s%c%s", cmd.TargetDictLoc, os.PathSeparator, ".replite")
	paramsJSON := fmt.Sprintf("%s%c%s", targetDict, os.PathSeparator, "params.json")
	if _, err := os.Open(paramsJSON); os.IsNotExist(err) {
		if _, err := os.Open(targetDict); os.IsNotExist(err) {
			os.Mkdir(targetDict, 0644)
		}
		paramsInfoBys, err := json.Marshal(cmd)
		if err != nil {
			panic(fmt.Sprintf("持久化用户参数信息时发生未知错误:%s", err.Error()))
		}
		paramsFile, err := os.OpenFile(paramsJSON, os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			panic(fmt.Sprintf("持久化未爬取的数据时发生未知错误:%s", err.Error()))
		}
		io.Copy(paramsFile, bytes.NewBuffer(paramsInfoBys))
	}
	return cmd
}

func newController(cmd *CmdParams) *controller {
	controller := new(controller)
	controller.finalizeSignal = make(chan error)
	// controller.proxyStatus = true
	controller.requestMutex = 0
	controller.mapRWMutex = sync.RWMutex{}
	controller.taskSignalMap = make(map[string]*VariableParams)
	controller.params = cmd
	controller.stdMutex = &sync.RWMutex{}
	controller.failedChan = &sync.RWMutex{}
	// controller.once = &sync.Once{}
	// analy the package
	fileStr := cmd.FileStr
	if cmd.Fixup != "" {
		fileStr = fmt.Sprintf("%s%c%s%c%s", cmd.Fixup, os.PathSeparator, ".replite", os.PathSeparator, "originPackage.txt")
	}
	file, err := os.Open(fileStr)
	if err != nil {
		panic(fmt.Sprintf("打开数据包txt文件(%s)出错:%s", cmd.FileStr, err.Error()))
	}
	controller.originPackage, err = io.ReadAll(file)
	if err != nil {
		panic(fmt.Sprintf("数据包txt文件(%s)读取失败:%s", cmd.FileStr, err.Error()))
	}
	controller.unlessRequest, err = http.ReadRequest(bufio.NewReader(bytes.NewReader(controller.originPackage)))
	if err != nil {
		panic(fmt.Sprintf("初始化生成request失败,数据包文件有误:%s", err.Error()))
	}
	controller.requestTemplateHandle()
	controller.buildCollector(&ProxyServer{
		Ip:       cmd.Ip,
		Port:     cmd.Port,
		Username: cmd.Username,
		Password: cmd.Password,
	}, withLimitRule(&colly.LimitRule{DomainGlob: controller.unlessRequest.Host, Delay: cmd.IntervalTime, Parallelism: cmd.ConcurrentNumber}), withRequestTimeout(defualtRequestTimeout))
	return controller
}

type repliteChain interface {
	setNext(repliteChain) repliteChain
	handle(*controller)
}

type baseRepliteChain struct {
	next repliteChain
}

func (base *baseRepliteChain) setNext(r repliteChain) repliteChain {
	base.next = r
	return base.next
}
func (base *baseRepliteChain) handle(repliteController *controller) {
	if base.next != nil {
		base.next.handle(repliteController)
	}
}

// var _ repliteChain = (*fixupChain)(nil)

type fixupChain struct {
	baseRepliteChain
}

func (fixup *fixupChain) handle(repliteController *controller) {
	if !(repliteController.params.Fixup != "" || repliteController.finish) {
		fixup.next.handle(repliteController)
		return
	}
	var isNewCookie = true
	//read file to keep the information for pre replite
	var newCookies = make([]*http.Cookie, 0, len(repliteController.unlessRequest.Cookies()))
	var curCookie = repliteController.unlessRequest.Cookies()
	fmt.Println("当前的Cookie为:")
	for i := 0; i < len(curCookie); i++ {
		fmt.Printf("%s\n", curCookie[i].String())
	}
	for {
		var cookie string
		var isNewCookieStr string
		fmt.Println("是否需要更换Cookie(true or false):")
		fmt.Scanln(&isNewCookieStr)
		if strings.ToLower(isNewCookieStr) != "true" {
			isNewCookie = false
			break
		}
		fmt.Printf("请重新输入Cookie:")
		fmt.Scanln(&cookie)
		vals := strings.Split(cookie, "=")
		if len(vals) != 2 {
			fmt.Printf("输入的cookie不符合规范(应该为key=value类型):%s\n", cookie)
			continue
		}
		// curCookie is visible cookies for user
		newCookies = append(newCookies, &http.Cookie{Name: vals[0], Value: vals[1]})
		fmt.Println("当前的Cookie为:")
		for i := 0; i < len(newCookies); i++ {
			fmt.Printf("%s\n", newCookies[i].String())
		}
		var continueFlag string
		fmt.Printf("是否继续输入cookie(true or false):")
		fmt.Scanln(&continueFlag)
		if strings.ToLower(continueFlag) != "true" {
			break
		}
	}
	if isNewCookie {
		repliteController.cookiesChange = true
		repliteController.cookies = cookiesToString(newCookies)
	}
	// if repliteController.finish is true represent use the fixup to inspect the all file size
	if !repliteController.finish {
		paramsFileStr := fmt.Sprintf("%s%c%s%c%s", repliteController.params.Fixup, os.PathSeparator, ".replite", os.PathSeparator, "unfinish.json")
		file, err := os.Open(paramsFileStr)
		if err != nil {
			fmt.Printf("%s此爬取的文件夹里的数据没有需要修复的\n", repliteController.params.Fixup)
			return
		}
		var variableParamsMap = make(map[string]*VariableParams)
		err = json.NewDecoder(bufio.NewReader(file)).Decode(&variableParamsMap)
		if err != nil {
			panic(fmt.Sprintf("%s反序列化出错的可变参数数据出错", paramsFileStr))
		}
		repliteController.taskSignalMap = variableParamsMap
	}
	begin := make(chan struct{}, 1)
	once := sync.Once{}
	var needRun bool = false
	if len(repliteController.taskSignalMap) > 0 {
		needRun = true
	}
	for _, value := range repliteController.taskSignalMap {
		go func(value *VariableParams) {
		recoverRequest:
			request, err := http.ReadRequest(bufio.NewReader(strings.NewReader(fmt.Sprintf("%s%s%s", string(repliteController.singleTemplate.prefix), value.Value, string(repliteController.singleTemplate.suffix)))))
			if err != nil {
				if errors.Is(err, &net.OpError{}) {
					atomic.AddInt32(&repliteController.opTolerance, 1)
				}
				if atomic.LoadInt32(&repliteController.opTolerance) > maxToleranceTimesForNetwork {
					repliteController.finalizeSignal <- fmt.Errorf("生成request失败:%s,已经恢复socks连接%d次", err.Error(), repliteController.opTolerance)
				} else {
					// if atomic.CompareAndSwapInt32(&repliteController.requestMutex, 0, 1) {
					// repliteController.retryConn()
					// 	time.Sleep(defaultSocksRecoverTime)
					// 	atomic.CompareAndSwapInt32(&repliteController.requestMutex, 1, 0)
					// }
					goto recoverRequest
				}
			}
			ctx := colly.NewContext()
			ctx.Put("id", strconv.Itoa(int(value.Id)))
			ctx.Put("value", value.Value)
			err = repliteController.c.Request(request.Method, request.URL.String(), request.Body, ctx, request.Header)
			if len(begin) == 0 {
				once.Do(func() {
					begin <- struct{}{}
					begin <- struct{}{}
				})
			}
			if err == nil {
				return
			}
			if _, ok := ignoreErrorMap[err]; err != nil && !ok {
				if strings.Contains(err.Error(), "the connected party did not properly respond after a period of time") || strings.Contains(err.Error(), " connected host has failed to respond") {
					return
				}
				repliteController.finalizeSignal <- fmt.Errorf("发送请求异常(%v) : %s", request, err.Error())
			}
		}(value)
		time.Sleep(smoothRequest)
	}
	if needRun {
		<-begin
	}
	repliteController.c.Wait()
}

type pageChain struct {
	baseRepliteChain
}

func (page *pageChain) handle(repliteController *controller) {
	if !repliteController.params.IsPage {
		page.next.handle(repliteController)
		return
	}
	// execute the page method
	once := sync.Once{}
	begin := make(chan struct{}, 1)
	var curForm = repliteController.params.From
	for curForm <= repliteController.params.To {
		index := strconv.Itoa(curForm)
		repliteController.taskSignalMap[index] = &VariableParams{
			Id:    int32(curForm),
			Value: index,
		}
		curForm += repliteController.params.Step
	}
	//to keep all params defend the error situation,when first execute
	if repliteController.params.Fixup == "" {
		repliteController.keepTaskVar()
	}
	var needRun bool = false
	// reset curFrom
	curForm = repliteController.params.From
	if curForm <= repliteController.params.To {
		needRun = true
	}
	for curForm <= repliteController.params.To {
		go func(from int) {
			fromVar := strconv.FormatInt(int64(from), 10)
			// fmt.Println(fmt.Sprintf("%s%s%s", string(repliteController.singleTemplate.prefix), fromVar, string(repliteController.singleTemplate.suffix)))
		recoverRequest:
			request, err := http.ReadRequest(bufio.NewReader(strings.NewReader(fmt.Sprintf("%s%s%s", string(repliteController.singleTemplate.prefix), fromVar, string(repliteController.singleTemplate.suffix)))))
			if err != nil {
				if errors.Is(err, &net.OpError{}) {
					atomic.AddInt32(&repliteController.opTolerance, 1)
				}
				if atomic.LoadInt32(&repliteController.opTolerance) > maxToleranceTimesForNetwork {
					repliteController.finalizeSignal <- fmt.Errorf("生成request失败:%s,已经恢复socks连接%d次", err.Error(), repliteController.opTolerance)
				} else {
					// if atomic.CompareAndSwapInt32(&repliteController.requestMutex, 0, 1) {
					// repliteController.retryConn()
					// 	time.Sleep(defaultSocksRecoverTime)
					// 	atomic.CompareAndSwapInt32(&repliteController.requestMutex, 1, 0)
					// }
					goto recoverRequest
				}
			}
			ctx := colly.NewContext()
			curPage := strconv.FormatInt(int64(from), 10)
			ctx.Put("id", curPage)
			ctx.Put("value", curPage)
			err = repliteController.c.Request(request.Method, request.URL.String(), request.Body, ctx, request.Header)
			if len(begin) == 0 {
				once.Do(func() {
					begin <- struct{}{}
					begin <- struct{}{}
				})
			}
			if err == nil {
				return
			}
			if _, ok := ignoreErrorMap[err]; err != nil && !ok {
				if strings.Contains(err.Error(), "the connected party did not properly respond after a period of time") || strings.Contains(err.Error(), " connected host has failed to respond") {
					return
				}
				repliteController.finalizeSignal <- fmt.Errorf("发送请求异常(%v) : %s", request, err.Error())
			}
		}(curForm)
		time.Sleep(smoothRequest)
		curForm += repliteController.params.Step
	}
	// ensure the go func(){} starting be executed
	// time.Sleep(2 * time.Millisecond)
	if needRun {
		<-begin
	}
	repliteController.c.Wait()
}

type listChain struct {
	baseRepliteChain
}

func (list *listChain) handle(repliteController *controller) {
	if !repliteController.params.IsList {
		list.next.handle(repliteController)
		return
	}
	//read file
	file, err := os.Open(repliteController.params.List)
	if err != nil {
		repliteController.finalizeSignal <- fmt.Errorf("打开list文件失败:%s", err.Error())
		return
	}
	// analy data for file
	fileByt, err := io.ReadAll(file)
	if err != nil {
		repliteController.finalizeSignal <- fmt.Errorf("读取list文件失败:%s", err.Error())
		return
	}
	lines := strings.Split(string(fileByt), "\r\n")
	var i = 0
	once := sync.Once{}
	begin := make(chan struct{}, 1)
	// todo presistent the data for  request panic
	for i := 0; i < len(lines); i++ {
		repliteController.taskSignalMap[strconv.Itoa(i)] = &VariableParams{
			Id:    int32(i),
			Value: lines[i],
		}
	}
	//to keep all params defend the error situation,when first execute
	if repliteController.params.Fixup == "" {
		repliteController.keepTaskVar()
	}
	var needRun bool = false
	if i < len(lines) {
		needRun = true
	}
	for i < len(lines) {
		// time.Sleep(time.Millisecond)
		go func(i int) {
		recoverRequest:
			request, err := http.ReadRequest(bufio.NewReader(strings.NewReader(fmt.Sprintf("%s%s%s", string(repliteController.singleTemplate.prefix), lines[i], string(repliteController.singleTemplate.suffix)))))
			if err != nil {
				if errors.Is(err, &net.OpError{}) {
					atomic.AddInt32(&repliteController.opTolerance, 1)
				}
				if atomic.LoadInt32(&repliteController.opTolerance) > maxToleranceTimesForNetwork {
					repliteController.finalizeSignal <- fmt.Errorf("生成request失败:%s,已经恢复socks连接%d次", err.Error(), repliteController.opTolerance)
				} else {
					// if atomic.CompareAndSwapInt32(&repliteController.requestMutex, 0, 1) {
					// repliteController.retryConn()
					// time.Sleep(defaultSocksRecoverTime)
					// 	atomic.CompareAndSwapInt32(&repliteController.requestMutex, 1, 0)
					// }
					goto recoverRequest
				}
			}
			ctx := colly.NewContext()
			ctx.Put("id", strconv.FormatInt(int64(i+1), 10))
			ctx.Put("value", lines[i])
			err = repliteController.c.Request(request.Method, request.URL.String(), request.Body, ctx, request.Header)
			if len(begin) == 0 {
				once.Do(func() {
					begin <- struct{}{}
					begin <- struct{}{}
				})
			}
			if err == nil {
				return
			}
			if _, ok := ignoreErrorMap[err]; err != nil && !ok {
				if strings.Contains(err.Error(), "the connected party did not properly respond after a period of time") || strings.Contains(err.Error(), " connected host has failed to respond") {
					return
				}
				repliteController.finalizeSignal <- fmt.Errorf("发送请求异常(%v) : %s", request, err.Error())
			}
		}(i)
		time.Sleep(smoothRequest)
		i++
	}
	// ensure the go func(){} starting be executed
	if needRun {
		<-begin
	}
	repliteController.c.Wait()

}

// func ToBase64(s string) string {
// 	return base64.StdEncoding.EncodeToString([]byte(s))
// }

type requestTemplate struct {
	prefix        []byte
	suffix        []byte
	originContent []byte
	// noCopy uintptr
}

// this page defend the concurrent operate to repliteController.taskSignalMap
func (repliteController *controller) keepTaskVar() {
	bys, err := json.Marshal(repliteController.taskSignalMap)
	if err != nil {
		panic(fmt.Sprintf("持久化全部可变参数文件出错:%s", err.Error()))
	}
	// already create by main function
	// dict := fmt.Sprintf("%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite")
	// var pathError *os.PathError
	// if _, err := os.Open(dict); errors.As(err, pathError) {
	// 	os.Mkdir(dict, 0644)
	// }
	fileStr := fmt.Sprintf("%s%c%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite", os.PathSeparator, "allTaskVar.json")
	file, err := os.OpenFile(fileStr, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(fmt.Sprintf("创建%s文件失败:%s", fileStr, err.Error()))
	}
	io.Copy(file, bytes.NewBuffer(bys))
}

// type request struct {
// 	single *requestTemplate
// 	// current only support the int32 type
// 	variable any
// }

// handle the originPackge to the requestTemplate and find the variable for package
func (repliteController *controller) requestTemplateHandle() {
	var reasonable = false
	// var i int = 0
	repliteController.singleTemplate = new(requestTemplate)
	for i := 0; i < len(repliteController.originPackage); i++ {
		if repliteController.originPackage[i] == 36 {
			repliteController.singleTemplate.prefix = make([]byte, i)
			copy(repliteController.singleTemplate.prefix, repliteController.originPackage[:i])
			// find the suffix
			for j := i + 1; j < len(repliteController.originPackage); j++ {
				if repliteController.originPackage[j] == 36 {
					repliteController.singleTemplate.suffix = make([]byte, len(repliteController.originPackage)-j)
					copy(repliteController.singleTemplate.suffix, repliteController.originPackage[j+1:])
					//originContent load
					repliteController.singleTemplate.originContent = make([]byte, j-i-1)
					copy(repliteController.singleTemplate.originContent, repliteController.originPackage[i+1:j])
					reasonable = true
					goto find
				}
			}
		}
	}
find:
	if !reasonable {
		panic("数据包txt文件未标注可变参数,请用$$标注可变参数")
	}
	// keep the origin package to next user
	var targetDict = fmt.Sprintf("%s%c%s", repliteController.params.TargetDictLoc, os.PathSeparator, ".replite")
	var targetFile = fmt.Sprintf("%s%c%s", targetDict, os.PathSeparator, "originPackage.txt")
	if _, err := os.Open(targetFile); os.IsNotExist(err) {
		if _, err := os.Open(targetDict); os.IsNotExist(err) {
			os.Mkdir(targetDict, 0644)
		}
		file, err := os.OpenFile(targetFile, os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			panic(fmt.Sprintf("持久化未爬取的数据时发生未知错误:%s", err.Error()))
		}
		io.Copy(file, bytes.NewBuffer(repliteController.originPackage))
	}
}
