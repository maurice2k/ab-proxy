# ab-proxy

ab-proxy is an Apache benchmark (ab) inspired tool with support for HTTP(S) and **SOCKS5 proxies**. It's main purpose is to benchmark proxy performance against a given HTTP endpoint.

## Installation
```
$ go get -u github.com/maurice2k/ab-proxy
$ $GOPATH/bin/ab-proxy -h
```

## Sample usage
```
$ ab-proxy -c 10 -n 200 --bursts 2 --proxy 'socks5://192.168.10.1' 'https://example.org'
```

## Available command line options
```
Usage:
  ab-proxy [OPTIONS] URL

Application Options:
  -c=<number>              Number of multiple requests to perform at a time. Default is one request at a time. (default: 1)
  -n=<number>              Number of requests to perform within a single burst. (default: 1)
      --bursts=<number>    Number of bursts (default: 1)
      --delay=<number>     Delay in seconds between bursts (default: 3)
  -X, --proxy=             Proxy URL (socks5://..., https://... or http://...)
  -s=<number>              Maximum time in seconds a complete HTTP request may take (0 means no limit) (default: 0)
      --user-agent=        Sets user agent (default: ab-proxy/1.0.0)
  -H, --header=            Add extra header to the request (i.e. "Accept-Encoding: 8bit")
      --show-errors        Show list of errors sorted by frequency (max. 100 unique errors)
      --version            Show version

Help Options:
  -h, --help               Show this help message
```
