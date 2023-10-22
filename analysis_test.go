package main

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"testing"
)

func TestAnalysis(b *testing.T) {
	cmd := CmdParams{
		List:     "data.txt",
		Port:     20924,
		Username: "20924",
		Password: "20924",
		FileStr:  "test.txt",
		Ip:       "43.154.180.200",
		IsList:   true,
	}
	controller := newController(&cmd)
	cValue := reflect.ValueOf(*controller)
	cType := reflect.TypeOf(*controller)
	fieldNum := cValue.NumField()
	for i := 0; i < fieldNum; i++ {
		curValue := cValue.Field(i)
		curType := cType.Field(i)
		fmt.Println(curType.Name + ":" + curValue.String())
	}
	// controller.executeRepliteChain()
}

func TestUmMarshalRequest(b *testing.T) {
	cmd := CmdParams{
		From:     1,
		To:       123,
		Port:     20924,
		Username: "20924",
		Password: "20924",
		FileStr:  "UmMarshalRequestPackage.txt",
		Ip:       "43.154.180.200",
		IsList:   true,
	}
	controller := newController(&cmd)
	var curFrom = cmd.From
	// originNumber, err := strconv.ParseInt(string(controller.singleTemplate.originContent), 10, 64)
	// if err != nil {
	// 	panic(err)
	// }
	for curFrom <= cmd.To {
		// fmt.Println("prefix:" + string(controller.singleTemplate.prefix))
		// fmt.Println("sufix:" + string(controller.singleTemplate.suffix))
		if curFrom == 10 {
			fmt.Println("there")
		}
		request, err := controller.ReadRequest(strconv.Itoa(curFrom))
		// request, err := http.ReadRequest(bufio.NewReader(strings.NewReader(fmt.Sprintf("%s%d%s", string(controller.singleTemplate.prefix), curFrom, string(controller.singleTemplate.suffix)))))
		if err != nil {
			panic(err)
		}
		fmt.Printf("%v", request)
		// fixNumberContentLength(curFrom, int(originNumber), request)
		// fmt.Println(request.Body)
		byt, err := io.ReadAll(request.Body)
		if err != nil {
			panic(err)
		}
		fmt.Println("bytes:" + string(byt))
		var Mapping = make(map[string]any, 10)
		if err = json.Unmarshal(byt, &Mapping); err != nil {
			panic(err)
		}
		for key, value := range Mapping {
			fmt.Printf("%s: %v\n", key, value)
		}
		curFrom++
	}
}

// func fixNumberContentLength(curNumber int, originNumber int, r *http.Request) {
// 	r.ContentLength = int64(calculateGap(curNumber, originNumber))
// }

// func calculateGap(curNumber int, originNumber int) int {
// 	var curLength = 0
// 	var originLength = 0
// 	var preNumber = curNumber
// 	for curNumber%10 != preNumber {
// 		preNumber = curNumber
// 		curNumber %= 10
// 		curLength++
// 	}
// 	preNumber = originNumber
// 	for originNumber%10 != preNumber {
// 		preNumber = originNumber
// 		originNumber %= 10
// 		originLength++
// 	}
// 	return curLength - originLength
// }
