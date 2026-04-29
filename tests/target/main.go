package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"
)

const bodyOK = "OK\n"

func handler(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())

	var statusCode int
	var body []byte
	var contentType string

	switch {
	case path == "/health":
		contentType = "text/plain"
		statusCode = 200
		body = []byte(bodyOK)

	case path == "/":
		contentType = "text/plain"
		statusCode = 200
		body = []byte(bodyOK)

	case strings.HasPrefix(path, "/size/"):
		n, err := strconv.Atoi(path[len("/size/"):])
		if err != nil || n < 0 {
			statusCode = 400
		} else {
			contentType = "application/octet-stream"
			statusCode = 200
			body = make([]byte, n)
		}

	case strings.HasPrefix(path, "/status/"):
		code, err := strconv.Atoi(path[len("/status/"):])
		if err != nil || code < 100 || code > 599 {
			statusCode = 400
		} else {
			statusCode = code
			body = []byte("status=" + strconv.Itoa(code))
		}

	case path == "/headers":
		headers := make(map[string]string)
		ctx.Request.Header.VisitAll(func(key, value []byte) {
			headers[string(key)] = string(value)
		})
		b, _ := json.Marshal(headers)
		contentType = "application/json"
		statusCode = 200
		body = b

	case path == "/echo":
		contentType = "text/plain"
		statusCode = 200
		body = []byte("OK\n")

	default:
		statusCode = 404
	}

	if contentType != "" {
		ctx.SetContentType(contentType)
	}
	ctx.SetStatusCode(statusCode)
	if body != nil {
		ctx.SetBody(body)
	}

	log.Printf("%s %d %d %s", ctx.RemoteAddr(), statusCode, len(body), path)
}

func main() {
	fmt.Println("target server listening on :80")
	if err := fasthttp.ListenAndServe(":80", handler); err != nil {
		panic(err)
	}
}
