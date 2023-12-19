package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

var messageTemplate = []OpenAIMessage{
	OpenAIMessage{Role: "system", Content: "你现在是一名精通计算机的人员,非常了解如何根据响应体判断登录是否失效,一般来说，对于系统繁忙,系统错误,点击次数过多这种没有明确表明登录失效的语句,那这次登录就没有失效,也就不需要重新登录"},
	OpenAIMessage{Role: "user", Content: "下面``````里面的内容是接收到的响应体信息，你需要根据响应体内容判断登录是否失效，响应体的部分信息有可能是编码后的结果，所以你需要先转码后再进行判断,你回答的结果只能是true或者false这两种答案,不需要另带解释,```%s```"},
}

type Params struct {
	Key               string  `json:"-"`
	Model             string  `json:"model"`
	Max_tokens        int     `json:"max_tokens"`
	Temperature       float64 `json:"temperature"`
	Top_p             float64 `json:"top_p"`
	Presence_penalty  int     `json:"presence_penalty"`
	Frequency_penalty int     `json:"frequency_penalty"`
	N                 int     `json:"n"`
	Stream            bool    `json:"-"`
	Stop              string  `json:"-"`
	// Logit_bias        map[string]interface{} `gorm:"type:json" json:"logit_bias"`
	Logit_bias map[string]interface{} `json:"-"`
}

// init for database
// current can use default configure
var defaultParams Params = Params{
	Presence_penalty:  0,
	Frequency_penalty: 1,
	Temperature:       0.5,
	Top_p:             0.3,
	Model:             "gpt-3.5-turbo",
	Key:               "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",
	Max_tokens:        1024,
	N:                 1,
}

func GetDefaultParams() Params {
	return defaultParams
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Name    string `json:"name"`
}
type ChatResponse struct {
	Id      string        `json:"id"`
	Object  string        `json:"object"`
	Created time.Duration `json:"created"`
	Choices []Choice      `json:"choices"`
	Usage   Usage         `json:"usage"`
}

type Choice struct {
	Index         int64         `json:"index"`
	Message       OpenAIMessage `json:"message"`
	Finish_reason string        `json:"finish_reason"`
}

type Usage struct {
	Prompt_tokens     int64 `json:"prompt_tokens"`
	Completion_tokens int64 `json:"completion_tokens"`
	Total_tokens      int64 `json:"total_tokens"`
}

type Body struct {
	Params Params `json:"params"`
	//default keep the 10 length for per session
	Messages []OpenAIMessage `json:"messages"`
}

type chatgptClient http.Client

var (
	singleChatgptClient *chatgptClient
	chatOnce            sync.Once
)

func GetChatgptClient() *chatgptClient {
	chatOnce.Do(func() {
		singleChatgptClient = &chatgptClient{}
	})
	return singleChatgptClient
}

/*
*

	must pre-packed
*/
func (client *chatgptClient) send(data *Body) (string, error) {
	req, err := packedRequest(data)
	if err != nil {
		return "", err
	}
	response, err := (*http.Client)(client).Do(req)
	if err != nil {
		return "", nil
	}
	result, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("%v response read error:%s", result, err.Error())
	}
	var resultMap ChatResponse
	err = json.NewDecoder(bytes.NewReader([]byte(result))).Decode(&resultMap)
	if err != nil {
		log.Println(err)
		return "", err
	}
	return string(resultMap.Choices[0].Message.Content), err
}

func (client *chatgptClient) SendQuestionRaw(message string) (string, error) {
	newMessage := make([]OpenAIMessage, len(messageTemplate))
	copy(newMessage, messageTemplate)
	newMessage[1].Content = fmt.Sprintf(newMessage[1].Content, message)
	newBody := &Body{
		Params:   defaultParams,
		Messages: newMessage,
	}
	return client.send(newBody)
}

// func (client *chatgptClient) sendRaw(data *Body) (*http.Response, error) {
// 	req, err := packedRequest(data)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return (*http.Client)(client).Do(req)
// }

const defaultChatgptURL = "https://api.openai-proxy.com/v1/chat/completions"

func packedRequest(data *Body) (*http.Request, error) {
	//update the request header
	var err error
	jsonData, err := data.Marshal()
	if err != nil {
		return nil, err
	}
	fmt.Println(string(jsonData))
	res, err := http.NewRequest(http.MethodPost, defaultChatgptURL, bufio.NewReader(bytes.NewReader(jsonData)))
	if err != nil {
		return nil, err
	}
	res.Header.Add("Content-Type", "application/json")
	res.Header.Add("Authorization", fmt.Sprintf("Bearer %s", data.Params.Key))
	return res, nil

}
func (body *Body) Marshal() ([]byte, error) {
	paramsResult, err := json.Marshal(&body.Params)
	if err != nil {
		return []byte{}, err
	}
	sb := strings.Builder{}
	sb.WriteString(string(paramsResult[:len(paramsResult)-1]))
	sb.WriteByte(',')

	messagesResult, err := json.Marshal(&body.Messages)
	if err != nil {
		return []byte{}, err
	}
	sb.WriteString("\"messages\":")
	sb.WriteString(string(messagesResult))
	sb.WriteString("}")
	return []byte(sb.String()), nil

}
