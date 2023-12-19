package main

import (
	"fmt"
	"testing"
)

func TestOpenAI(t *testing.T) {
	resp, err := GetChatgptClient().SendQuestionRaw("{data:\"print\",info:\"请你先登录\"}")
	if err != nil {
		panic(err)
	}
	// var body = new(ChatResponse)
	// respByt, _ := io.ReadAll(resp.Body)

	fmt.Printf("%s", resp)
}

func TestSpark(t *testing.T) {
	context, err := SendQuestion("{data:\"print\",info:\"\u8bf7\u91cd\u65b0\u767b\u5f55\"}")
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s", context)
}
