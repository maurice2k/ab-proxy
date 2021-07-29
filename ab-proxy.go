// Copyright 2019 Moritz Fain
// Moritz Fain <moritz@fain.io>
package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/schollz/progressbar/v2"
)

const version string = "1.0.0"

var stopChan = make(chan os.Signal, 1)

// error list handling
const maxUniqueErrors = 100

var errChan = make(chan error, 10000)
var errMap = make(map[string]int)
var errWg sync.WaitGroup

type errItem struct {
	cnt    int
	errMsg string
}

type errListByCnt []errItem

func (a errListByCnt) Len() int           { return len(a) }
func (a errListByCnt) Less(i, j int) bool { return a[i].cnt < a[j].cnt }
func (a errListByCnt) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// flags definition
var mainOpts struct {
	Concurrency int      `short:"c" description:"Number of multiple requests to perform at a time. Default is one request at a time." default:"1" value-name:"<number>"`
	Requests    int      `short:"n" description:"Number of requests to perform within a single burst." default:"1" value-name:"<number>"`
	Bursts      int      `long:"bursts" description:"Number of bursts" default:"1" value-name:"<number>"`
	Delay       int      `long:"delay" description:"Delay in seconds between bursts" default:"3" value-name:"<number>"`
	Proxy       string   `short:"X" long:"proxy" description:"Proxy URL (socks5://..., https://... or http://...)" required:"no"`
	Timeout     int      `short:"s" description:"Maximum time in seconds a complete HTTP request may take (0 means no limit)" value-name:"<number>" default:"0"`
	UserAgent   string   `long:"user-agent" description:"Sets user agent" default:"ab-proxy/1.0.0"`
	Header      []string `short:"H" long:"header" description:"Add extra header to the request (i.e. \"Accept-Encoding: 8bit\")"`
	ShowErrors  bool     `long:"show-errors" description:"Show list of errors sorted by frequency (max. 100 unique errors)"`
	Version     bool     `long:"version" description:"Show version"`
	Args        struct {
		Url string `positional-arg-name:"URL"`
	} `positional-args:"yes" required:"yes"`
}

// Prints error message and exists with given exitCode
func exitWithErrorMsg(exitCode int, message string, replacements ...interface{}) {
	if len(replacements) > 0 {
		message = fmt.Sprintf(message, replacements...)
	}
	fmt.Fprintln(os.Stderr, message)
	os.Exit(exitCode)
}

func main() {
	var bar *progressbar.ProgressBar

	// Subscribe to signals
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)

	// Parse flags and arguments
	parser := flags.NewParser(&mainOpts, flags.HelpFlag)
	_, err := parser.Parse()

	if mainOpts.Version {
		fmt.Println(version)
		os.Exit(0)
	}

	if err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			// normal help behaviour
		} else {
			fmt.Println("Usage error:", err)
			fmt.Println()
		}
		parser.WriteHelp(os.Stdout)
		os.Exit(1)
	}

	// start error err channel handler in case we want to display errors
	if mainOpts.ShowErrors {
		go func() {
			errWg.Add(1)

			for err := range errChan {
				if len(errMap) > maxUniqueErrors {
					continue
				}

				errStr := err.Error()
				_, ok := errMap[errStr]
				if ok {
					errMap[errStr] += 1
				} else {
					errMap[errStr] = 1
				}
			}

			errWg.Done()
		}()
	}

	// check given URL
	if matched, _ := regexp.MatchString(`^\w+://`, mainOpts.Args.Url); !matched {
		mainOpts.Args.Url = "http://" + mainOpts.Args.Url
	}
	_, err = url.Parse(mainOpts.Args.Url)
	if err != nil {
		exitWithErrorMsg(2, "Invalid URL given: %s", err)
	}

	// prepare headers
	var headers textproto.MIMEHeader
	mainOpts.Header = append(mainOpts.Header, "User-Agent: " + mainOpts.UserAgent)
	tp := textproto.NewReader(bufio.NewReader(strings.NewReader(strings.Join(mainOpts.Header, "\r\n") + "\r\n\r\n")))
	headers, err = tp.ReadMIMEHeader()
	if err != nil {
		exitWithErrorMsg(3, "Unable to parse custom headers: %s", err)
	}

	// setup transport
	var tr *http.Transport = nil

	if mainOpts.Proxy != "" {
		if matched, _ := regexp.MatchString(`^\w+://`, mainOpts.Proxy); !matched {
			mainOpts.Proxy = "http://" + mainOpts.Proxy
		}

		uri, err := url.Parse(mainOpts.Proxy)
		if err != nil {
			exitWithErrorMsg(4, "Unable to parse proxy URL: %s", err)
		}

		if uri.Scheme == "socks5" {

			tr = &http.Transport{
				Proxy: http.ProxyURL(uri),
			}

		} else if uri.Scheme == "https" || uri.Scheme == "http" || uri.Scheme == "" {

			tr = &http.Transport{
				ProxyConnectHeader: http.Header(headers),
				Proxy:              http.ProxyURL(uri),
				// Disable HTTP/2.
				TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
			}

		} else {
			exitWithErrorMsg(5, "Unable to handle proxy with scheme '%s'", uri.Scheme)
		}

	}

	var stats struct {
		Requests              int64
		RequestsCompleted     int64
		RequestsCompletedCode [1000]int64
		RequestsFailed        int64
		FailedProxyAuth       int64
		FailedTimeout         int64
		BytesTransferred      int64
		StartTime             time.Time
		EndTime               time.Time
	}

	totalRequests := mainOpts.Bursts * mainOpts.Requests

	if mainOpts.Proxy != "" {
		fmt.Printf("Benchmarking '%s' using proxy '%s' with a total of %d GET requests:\n\n", mainOpts.Args.Url, mainOpts.Proxy, totalRequests)
	} else {
		fmt.Printf("Benchmarking '%s' with a total of %d GET requests:\n\n", mainOpts.Args.Url, totalRequests)
	}

	// progress bar initialization
	bar = progressbar.NewOptions(totalRequests,
		progressbar.OptionClearOnFinish(),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionSetDescription("Progress"),
		progressbar.OptionSetTheme(progressbar.Theme{Saucer: "=", SaucerPadding: "-", BarStart: "[", BarEnd: "]", SaucerHead: ">"}),
	)

	go func() {
		lastNum := 0
		for {
			select {
			case <-stopChan:
				// wait for SIGINT, SIGHUP, SIGTERM
				stopChan = nil
				fmt.Printf("\nStopping benchmark...\n\n")
				signal.Reset(syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
			default:
				// process progress bar
				curNum := int(stats.RequestsCompleted + stats.RequestsFailed)

				if curNum == totalRequests || stopChan == nil {
					break
				}

				if curNum - lastNum > 0 {
					bar.Add(curNum - lastNum)
				}

				time.Sleep(50 * time.Millisecond)
				lastNum = curNum
			}
		}
	}()

	// start benchmarking

	stats.StartTime = time.Now()

	for b := 0; b < mainOpts.Bursts && stopChan != nil; b++ {
		var requestsLeft int64
		var wg sync.WaitGroup

		atomic.AddInt64(&requestsLeft, int64(mainOpts.Requests))

		for c := 0; c < mainOpts.Concurrency && stopChan != nil; c++ {
			wg.Add(1)

			go func() {
				for {
					r := atomic.AddInt64(&requestsLeft, -1)
					if r < 0 || stopChan == nil {
						break
					}

					atomic.AddInt64(&stats.Requests, 1)

					hc := &http.Client{
						Timeout: time.Duration(mainOpts.Timeout) * time.Second,
					}

					if tr != nil {
						hc.Transport = tr
					}

					var err error
					var resp *http.Response
					req, err := http.NewRequest("GET", mainOpts.Args.Url, nil)
					if err == nil {
						req.Header = http.Header(headers)
						resp, err = hc.Do(req)
					}

					if err != nil {
						if mainOpts.ShowErrors {
							errChan <- err
						}

						atomic.AddInt64(&stats.RequestsFailed, 1)

						if mainOpts.Proxy != "" {
							if strings.Contains(err.Error(), "authentication") ||
								strings.Contains(err.Error(), "username/password") {
								atomic.AddInt64(&stats.FailedProxyAuth, 1)
							}

						}

						if urlErr, ok := err.(*url.Error); ok {
							if urlErr.Timeout() {
								atomic.AddInt64(&stats.FailedTimeout, 1)
							}
						}

						continue
					}

					errWhileReading := false
					for {
						slice := make([]byte, 128*1024)
						n, err := resp.Body.Read(slice)
						atomic.AddInt64(&stats.BytesTransferred, int64(n))
						if err == io.EOF {
							break

						} else if err != nil {
							if mainOpts.ShowErrors {
								errChan <- err
							}

							atomic.AddInt64(&stats.RequestsFailed, 1)
							if netErr, ok := err.(net.Error); ok {
								if netErr.Timeout() {
									atomic.AddInt64(&stats.FailedTimeout, 1)
								}
							}

							errWhileReading = true
							break
						}
					}

					resp.Body.Close()


					if errWhileReading {
						continue
					}

					atomic.AddInt64(&stats.RequestsCompleted, 1)
					if resp.StatusCode >= 0 && resp.StatusCode <= 999 {
						atomic.AddInt64(&stats.RequestsCompletedCode[resp.StatusCode], 1)
					} else {
						atomic.AddInt64(&stats.RequestsCompletedCode[0], 1)
					}


				}
				wg.Done()
			}()

		}

		wg.Wait()
		if b+1 < mainOpts.Bursts && stopChan != nil {
			time.Sleep(time.Second * time.Duration(mainOpts.Delay))
		}
	}

	bar.Finish()

	stats.EndTime = time.Now()
	elapsed := stats.EndTime.Sub(stats.StartTime)

	// print results

	fmt.Printf("Number of bursts:             %d\n", mainOpts.Bursts)
	fmt.Printf("Number of request per burst   %d\n", mainOpts.Requests)
	fmt.Printf("Concurrency level:            %d\n", mainOpts.Concurrency)
	fmt.Printf("Time taken for tests:         %s\n\n", elapsed)

	fmt.Printf("Total initiated requests:     %d\n", stats.Requests)
	fmt.Printf("   Completed requests:        %d\n", stats.RequestsCompleted)
	for c := range stats.RequestsCompletedCode {
		if stats.RequestsCompletedCode[c] > 0 {
			fmt.Printf("      HTTP-%03d completed:     %d\n", c, stats.RequestsCompletedCode[c])
		}
	}

	fmt.Printf("   Failed requests:           %d\n", stats.RequestsFailed)
	if stats.FailedProxyAuth > 0 {
		fmt.Printf("      Proxy auth failures:    %d\n", stats.FailedProxyAuth)
	}

	if stats.FailedTimeout > 0 {
		fmt.Printf("      Timeout failures:       %d\n", stats.FailedTimeout)
	}

	fmt.Printf("\nTotal transferred:            %d bytes\n", stats.BytesTransferred)

	if stats.Requests > 0 {
		timePerReq := time.Duration(elapsed / time.Duration(stats.Requests))
		reqPerSec := float32(float32(time.Second) / float32(timePerReq))
		fmt.Printf("Requests per second:          %.3f\n", reqPerSec)
		fmt.Printf("Time per request:             %s\n", timePerReq)
	}

	if mainOpts.ShowErrors {
		close(errChan)
		errWg.Wait() // wait until all errors from the errChan have been set to the errMap

		if len(errMap) > 0 {
			// generate sortable list of errMap
			var errList []errItem
			for errMsg, cnt := range errMap {
				errList = append(errList, errItem{cnt, errMsg})
			}

			sort.Sort(sort.Reverse(errListByCnt(errList)))

			fmt.Printf("\nErrors:\n")
			errPrintf := "% " + strconv.Itoa(len(strconv.Itoa(int(errList[0].cnt)))+1) + "dx  %s\n"

			for idx, err := range errList {
				if idx == maxUniqueErrors {
					fmt.Printf("... (list truncated)\n")
					break
				}
				fmt.Printf(errPrintf, err.cnt, err.errMsg)
			}
		}
	}
}
