package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	hostUrl   = "wss://spark-api.xf-yun.com/v3.1/chat"
	appid     = ""
	apiSecret = ""
	apiKey    = ""
)
var (
	sparkConn *websocket.Conn
	sparkOnce sync.Once
)

func getSparkClient() *websocket.Conn {
	sparkOnce.Do(func() {
		sparkClient := &websocket.Dialer{
			HandshakeTimeout: 5 * time.Second,
		}
		var resp *http.Response
		var err error
		sparkConn, resp, err = sparkClient.Dial(assembleAuthUrl1(hostUrl, apiKey, apiSecret), nil)
		if err != nil {
			panic(readResp(resp) + err.Error())
		} else if resp.StatusCode != 101 {
			panic(readResp(resp) + err.Error())
		}
	})
	return sparkConn
}

var sparkMessageTemplate = []SparkMessage{
	SparkMessage{Role: "system", Content: "你现在是一名精通计算机知识的人员,了解系统设计的思路,尤其是在登录这块,登录失效的处理的各种方式你都熟练掌握"},
	SparkMessage{Role: "user", Content: "下面``````里面的内容是接收到的响应体信息，你需要根据响应体信息判断本次登录是否失效，响应体的部分信息有可能是编码后的结果，所以你需要先转码后再进行判断,你回答的结果只能是true或者false,不要添加其他东西```%s```"},
}
var mutex sync.Mutex

func SendQuestion(message string) (string, error) {
	mutex.Lock()
	defer mutex.Unlock()
	conn := getSparkClient()
	params := genParams(appid, message)
	fmt.Println(params)
	err := conn.WriteJSON(params)
	if err != nil {
		return "", err
	}
	var answer = ""
	//获取返回的数据
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return "", fmt.Errorf("read message error:%v\n", err)
		}

		var data map[string]interface{}
		err1 := json.Unmarshal(msg, &data)
		if err1 != nil {
			return "", fmt.Errorf("Error parsing JSON:%v \n", err)

		}
		//解析数据
		payload := data["payload"].(map[string]interface{})
		choices := payload["choices"].(map[string]interface{})
		header := data["header"].(map[string]interface{})
		code := header["code"].(float64)

		if code != 0 {
			return "", fmt.Errorf("error payload:%v\n", data)
		}
		status := choices["status"].(float64)
		text := choices["text"].([]interface{})
		content := text[0].(map[string]interface{})["content"].(string)
		if status != 2 {
			answer += content
		} else {
			answer += content
			break
		}
	}
	return answer, nil
}

// 生成参数
func genParams(appid string, message string) map[string]interface{} { // 根据实际情况修改返回的数据结构和字段名
	newMessage := make([]SparkMessage, len(sparkMessageTemplate))
	copy(newMessage, sparkMessageTemplate)
	newMessage[1].Content = fmt.Sprintf(newMessage[1].Content, message)
	data := map[string]interface{}{ // 根据实际情况修改返回的数据结构和字段名
		"header": map[string]interface{}{ // 根据实际情况修改返回的数据结构和字段名
			"app_id": appid, // 根据实际情况修改返回的数据结构和字段名
		},
		"parameter": map[string]interface{}{ // 根据实际情况修改返回的数据结构和字段名
			"chat": map[string]interface{}{ // 根据实际情况修改返回的数据结构和字段名
				"domain":      "general",    // 根据实际情况修改返回的数据结构和字段名
				"temperature": float64(0.1), // 根据实际情况修改返回的数据结构和字段名
				"top_k":       int64(4),     // 根据实际情况修改返回的数据结构和字段名
				"max_tokens":  int64(2048),  // 根据实际情况修改返回的数据结构和字段名
				"auditing":    "default",    // 根据实际情况修改返回的数据结构和字段名
			},
		},
		"payload": map[string]interface{}{ // 根据实际情况修改返回的数据结构和字段名
			"message": map[string]interface{}{ // 根据实际情况修改返回的数据结构和字段名
				"text": newMessage, // 根据实际情况修改返回的数据结构和字段名
			},
		},
	}
	return data // 根据实际情况修改返回的数据结构和字段名
}

// 创建鉴权url  apikey 即 hmac username
func assembleAuthUrl1(hosturl string, apiKey, apiSecret string) string {
	ul, err := url.Parse(hosturl)
	if err != nil {
		fmt.Println(err)
	}
	//签名时间
	date := time.Now().UTC().Format(time.RFC1123)
	//date = "Tue, 28 May 2019 09:10:42 MST"
	//参与签名的字段 host ,date, request-line
	signString := []string{"host: " + ul.Host, "date: " + date, "GET " + ul.Path + " HTTP/1.1"}
	//拼接签名字符串
	sgin := strings.Join(signString, "\n")
	// fmt.Println(sgin)
	//签名结果
	sha := HmacWithShaTobase64("hmac-sha256", sgin, apiSecret)
	// fmt.Println(sha)
	//构建请求参数 此时不需要urlencoding
	authUrl := fmt.Sprintf("hmac username=\"%s\", algorithm=\"%s\", headers=\"%s\", signature=\"%s\"", apiKey,
		"hmac-sha256", "host date request-line", sha)
	//将请求参数使用base64编码
	authorization := base64.StdEncoding.EncodeToString([]byte(authUrl))

	v := url.Values{}
	v.Add("host", ul.Host)
	v.Add("date", date)
	v.Add("authorization", authorization)
	//将编码后的字符串url encode后添加到url后面
	callurl := hosturl + "?" + v.Encode()
	return callurl
}

func HmacWithShaTobase64(algorithm, data, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(data))
	encodeData := mac.Sum(nil)
	return base64.StdEncoding.EncodeToString(encodeData)
}

func readResp(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("code=%d,body=%s", resp.StatusCode, string(b))
}

type SparkMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
