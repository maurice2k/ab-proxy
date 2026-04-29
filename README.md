# ab-proxy

ab-proxy is an Apache benchmark (ab) inspired tool with support for HTTP(S) and **SOCKS5 proxies**. It's main purpose is to benchmark proxy performance against a given HTTP endpoint.

## Installation
```
$ go install github.com/maurice2k/ab-proxy@latest
$ ab-proxy -h
```

## Sample usage
```
$ ab-proxy -c 10 -n 200 --bursts 2 --proxy 'socks5://192.168.10.1' 'https://example.org'
```

Use `--json` for machine-readable output:
```
$ ab-proxy --json -n 50 -X socks5://proxy:1080 http://target/ | jq .
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
      --json               Output results as JSON
      --show-errors        Show list of errors sorted by frequency (max. 100 unique errors)
      --version            Show version
  -B, --bind=<address>     Bind outgoing connections to local address
  -i                       Use HEAD instead of GET
  -p, --post-file=<file>   File containing data to POST
  -u, --put-file=<file>    File containing data to PUT
      --tls-min=<version>  Minimum TLS version (1.2, 1.3)
      --tls-max=<version>  Maximum TLS version (1.2, 1.3)
      --tls-cipher=<name>  Allowed TLS cipher suite (repeatable)
      --tls-insecure      Skip TLS certificate verification
      --latency           Output latency distribution percentiles

Help Options:
  -h, --help               Show this help message
```

## Testing

Integration tests use Docker Compose with a target server and [moproxy](https://github.com/maurice2k/moproxy) as the proxy:

```
$ ./tests/run_tests.sh
$ ./tests/run_tests.sh --logs   # include container logs
```


