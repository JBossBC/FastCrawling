// package main

// import (
// 	"bufio"
// 	"io"
// 	"net/http"
// 	"strings"

// 	"golang.org/x/net/http2"
// 	"golang.org/x/net/http2/hpack"
// )

// type httpAdapter interface {
// 	ReadRequest(b *bufio.Reader) (*http.Request, error)
// }

// type httpAdapterImpl struct {
// }

// func (adapter *httpAdapterImpl) ReadRequest(b *bufio.Reader) (*http.Request, error) {
// 	byt, err := io.ReadAll(b)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if strings.Contains(string(byt), "HTTP/2.0") {
// 		return readRequest(b)
// 	}
// 	return http.ReadRequest(b)
// }

// func readRequest(b *bufio.Reader) (req *http.Request, err error) {
// 	http2Reader := http2.NewReader(reader, new(hpack.Decoder))
// 	return http.ReadRequest(http2Reader)

// }
