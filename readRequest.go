package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

func badStringError(what, val string) error { return fmt.Errorf("%s %q", what, val) }

var (
	singleRequest http.Request = http.Request{
		Header: make(http.Header),
	}
)

func singleParse(byt []byte) (err error) {
	slice := bytes.SplitN(byt, []byte("\r\n"), 2)
	line := string(slice[0])
	remain := slice[1]
	//analysis line
	var ok bool
	singleRequest.Method, singleRequest.RequestURI, singleRequest.Proto, ok = parseRequestLine(line)
	if !ok {
		return badStringError("malformed HTTP request", line)
	}
	if !validMethod(singleRequest.Method) {
		return badStringError("invalid method", singleRequest.Method)
	}
	rawurl := singleRequest.RequestURI
	if singleRequest.ProtoMajor, singleRequest.ProtoMinor, ok = ParseHTTPVersion(singleRequest.Proto); !ok {
		return badStringError("malformed HTTP version", singleRequest.Proto)
	}
	justAuthority := singleRequest.Method == "CONNECT" && !strings.HasPrefix(rawurl, "/")
	if justAuthority {
		rawurl = "http://" + rawurl
	}

	if singleRequest.URL, err = url.ParseRequestURI(rawurl); err != nil {
		return err
	}

	if justAuthority {
		// Strip the bogus "http://" back off.
		singleRequest.URL.Scheme = ""
	}
	NotLine := bytes.SplitN(remain, []byte("\r\n\r\n"), 2)
	rowHeaders := NotLine[0]
	// body := NotLine[1]
	headers := bytes.Split(rowHeaders, []byte("\r\n"))
	for i := 0; i < len(headers); i++ {
		rowHeader := bytes.SplitN(headers[i], []byte(":"), 2)
		value, _ := strings.CutPrefix(string(rowHeader[1]), " ")
		singleRequest.Header.Add(string(rowHeader[0]), value)
	}
	singleRequest.Close = shouldClose(singleRequest.ProtoMajor, singleRequest.ProtoMinor, singleRequest.Header, false)
	return nil
}

func shouldClose(major, minor int, header http.Header, removeCloseHeader bool) bool {
	if major < 1 {
		return true
	}

	conv := header["Connection"]
	hasClose := httpguts.HeaderValuesContainsToken(conv, "close")
	if major == 1 && minor == 0 {
		return hasClose || !httpguts.HeaderValuesContainsToken(conv, "keep-alive")
	}

	if hasClose && removeCloseHeader {
		header.Del("Connection")
	}

	return hasClose
}

func ParseHTTPVersion(vers string) (major, minor int, ok bool) {
	switch vers {
	case "HTTP/1.1":
		return 1, 1, true
	case "HTTP/1.0":
		return 1, 0, true
	//current cant support http2.0
	case "HTTP/2.0":
		return 2, 0, true
	}
	if !strings.HasPrefix(vers, "HTTP/") {
		return 0, 0, false
	}
	if len(vers) != len("HTTP/X.Y") {
		return 0, 0, false
	}
	if vers[6] != '.' {
		return 0, 0, false
	}
	maj, err := strconv.ParseUint(vers[5:6], 10, 0)
	if err != nil {
		return 0, 0, false
	}
	min, err := strconv.ParseUint(vers[7:8], 10, 0)
	if err != nil {
		return 0, 0, false
	}
	return int(maj), int(min), true
}

func isNotToken(r rune) bool {
	return !httpguts.IsTokenRune(r)
}
func validMethod(method string) bool {
	/*
	     Method         = "OPTIONS"                ; Section 9.2
	                    | "GET"                    ; Section 9.3
	                    | "HEAD"                   ; Section 9.4
	                    | "POST"                   ; Section 9.5
	                    | "PUT"                    ; Section 9.6
	                    | "DELETE"                 ; Section 9.7
	                    | "TRACE"                  ; Section 9.8
	                    | "CONNECT"                ; Section 9.9
	                    | extension-method
	   extension-method = token
	     token          = 1*<any CHAR except CTLs or separators>
	*/
	return len(method) > 0 && strings.IndexFunc(method, isNotToken) == -1
}
func parseRequestLine(line string) (method, requestURI, proto string, ok bool) {
	method, rest, ok1 := strings.Cut(line, " ")
	requestURI, proto, ok2 := strings.Cut(rest, " ")
	if !ok1 || !ok2 {
		return "", "", "", false
	}
	return method, requestURI, proto, true
}
