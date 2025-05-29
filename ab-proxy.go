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

var mainStopChan = make(chan os.Signal, 1) // Renamed to avoid conflict if passed as param

// ErrorCollector manages the collection and processing of errors during the benchmark.
type ErrorCollector struct {
	errChan         chan error
	errMap          map[string]int
	wg              sync.WaitGroup
	maxUniqueErrors int
	showErrors      bool
}

// NewErrorCollector creates and initializes an ErrorCollector.
func NewErrorCollector(bufferSize int, maxUnique int, show bool) *ErrorCollector {
	return &ErrorCollector{
		errChan:         make(chan error, bufferSize),
		errMap:          make(map[string]int),
		maxUniqueErrors: maxUnique,
		showErrors:      show,
	}
}

// Start launches the error collection goroutine.
func (ec *ErrorCollector) Start() {
	if !ec.showErrors {
		return
	}
	ec.wg.Add(1)
	go func() {
		defer ec.wg.Done()
		for err := range ec.errChan {
			errStr := err.Error()
			_, exists := ec.errMap[errStr]
			if exists {
				ec.errMap[errStr]++
			} else {
				// New unique error
				if len(ec.errMap) < ec.maxUniqueErrors {
					ec.errMap[errStr] = 1
				}
				// If len(ec.errMap) is already at maxUniqueErrors, this new unique error is ignored.
				// We continue to consume from errChan regardless.
			}
		}
	}()
}

// Stop signals the error collection goroutine to finish and waits for it.
func (ec *ErrorCollector) Stop() {
	if !ec.showErrors {
		return
	}
	close(ec.errChan)
	ec.wg.Wait()
}

// Add records an error.
func (ec *ErrorCollector) Add(err error) {
	if !ec.showErrors {
		return
	}
	ec.errChan <- err
}

// GetSortedErrors returns a sorted list of unique errors.
func (ec *ErrorCollector) GetSortedErrors() []errItem {
	if !ec.showErrors || len(ec.errMap) == 0 {
		return nil
	}
	var errList []errItem
	for errMsg, cnt := range ec.errMap {
		errList = append(errList, errItem{cnt, errMsg})
	}
	sort.Sort(sort.Reverse(errListByCnt(errList)))
	return errList
}

// Stats holds the benchmark statistics.
type Stats struct {
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

// error list handling
const maxUniqueErrors = 100 // This can be part of ErrorCollector if desired, or passed to NewErrorCollector

type errItem struct {
	cnt    int
	errMsg string
}

type errListByCnt []errItem

func (a errListByCnt) Len() int           { return len(a) }
func (a errListByCnt) Less(i, j int) bool { return a[i].cnt < a[j].cnt }
func (a errListByCnt) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// Options holds the command-line options.
type Options struct {
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

var mainOpts Options // Changed to use the new Options type

// Prints error message and exists with given exitCode
func exitWithErrorMsg(exitCode int, message string, replacements ...interface{}) {
	if len(replacements) > 0 {
		message = fmt.Sprintf(message, replacements...)
	}
	fmt.Fprintln(os.Stderr, message)
	os.Exit(exitCode)
}

// runBenchmark encapsulates the core benchmarking logic.
// It returns the collected statistics and an error if a fatal setup issue occurs.
func runBenchmark(opts Options, stopChan chan os.Signal, errCollector *ErrorCollector) (*Stats, error) {
	var bar *progressbar.ProgressBar
	stats := &Stats{} // Initialize stats

	// URL validation
	if matched, _ := regexp.MatchString(`^\w+://`, opts.Args.Url); !matched {
		opts.Args.Url = "http://" + opts.Args.Url
	}
	_, err := url.Parse(opts.Args.Url)
	if err != nil {
		return nil, fmt.Errorf("invalid URL given: %w", err)
	}

	// Header preparation
	var headers textproto.MIMEHeader
	actualHeaders := append([]string(nil), opts.Header...) // Create a mutable copy
	actualHeaders = append(actualHeaders, "User-Agent: "+opts.UserAgent)
	tp := textproto.NewReader(bufio.NewReader(strings.NewReader(strings.Join(actualHeaders, "\r\n") + "\r\n\r\n")))
	headers, err = tp.ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("unable to parse custom headers: %w", err)
	}

	// Transport setup
	var tr *http.Transport = nil
	if opts.Proxy != "" {
		proxyURLStr := opts.Proxy
		if matched, _ := regexp.MatchString(`^\w+://`, proxyURLStr); !matched {
			proxyURLStr = "http://" + proxyURLStr
		}
		uri, err := url.Parse(proxyURLStr)
		if err != nil {
			return nil, fmt.Errorf("unable to parse proxy URL: %w", err)
		}
		if uri.Scheme == "socks5" {
			tr = &http.Transport{Proxy: http.ProxyURL(uri)}
		} else if uri.Scheme == "https" || uri.Scheme == "http" || uri.Scheme == "" {
			tr = &http.Transport{
				Proxy:        http.ProxyURL(uri),
				TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
			}
		} else {
			return nil, fmt.Errorf("unable to handle proxy with scheme '%s'", uri.Scheme)
		}
	}

	totalRequests := opts.Bursts * opts.Requests
	// Output initial message (can be moved to main if preferred)
	if opts.Proxy != "" {
		fmt.Printf("Benchmarking '%s' using proxy '%s' with a total of %d GET requests:\n\n", opts.Args.Url, opts.Proxy, totalRequests)
	} else {
		fmt.Printf("Benchmarking '%s' with a total of %d GET requests:\n\n", opts.Args.Url, totalRequests)
	}

	bar = progressbar.NewOptions(totalRequests,
		progressbar.OptionClearOnFinish(),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionSetDescription("Progress"),
		progressbar.OptionSetTheme(progressbar.Theme{Saucer: "=", SaucerPadding: "-", BarStart: "[", BarEnd: "]", SaucerHead: ">"}),
	)

	// Progress bar update goroutine
	var progressWg sync.WaitGroup
	progressWg.Add(1)
	go func() {
		defer progressWg.Done()
		lastNum := 0
		for {
			select {
			case <-time.After(50 * time.Millisecond): // Check periodically
				// process progress bar
				curNum := int(atomic.LoadInt64(&stats.RequestsCompleted) + atomic.LoadInt64(&stats.RequestsFailed))
				if curNum-lastNum > 0 {
					bar.Add(curNum - lastNum)
				}
				lastNum = curNum
				if curNum >= totalRequests || stopChan == nil { // Check if stopChan became nil
					return
				}
			case _, ok := <-stopChan: // Listen to stopChan directly
				if !ok { // Channel closed by main or another signal
					return
				}
				// This path means an OS signal was received.
				// The stopChan is made nil later in main to signal benchmark loops to stop.
				// The bar update should continue until requests stop accumulating.
				// No specific action here other than letting the select loop again or exit if curNum >= totalRequests.
			}
		}
	}()

	stats.StartTime = time.Now()

	for b := 0; b < opts.Bursts && stopChan != nil; b++ {
		var requestsLeft int64
		var burstWg sync.WaitGroup

		atomic.StoreInt64(&requestsLeft, int64(opts.Requests))

		for c := 0; c < opts.Concurrency && stopChan != nil; c++ {
			burstWg.Add(1)
			go func() {
				defer burstWg.Done()
				for {
					r := atomic.AddInt64(&requestsLeft, -1)
					if r < 0 || stopChan == nil {
						break
					}

					atomic.AddInt64(&stats.Requests, 1)

					hc := &http.Client{Timeout: time.Duration(opts.Timeout) * time.Second}
					if tr != nil {
						hc.Transport = tr
					}

					var reqErr error
					var resp *http.Response
					req, reqErr := http.NewRequest("GET", opts.Args.Url, nil)
					if reqErr == nil {
						req.Header = http.Header(headers)
						resp, reqErr = hc.Do(req)
					}

					if reqErr != nil {
						errCollector.Add(reqErr)
						atomic.AddInt64(&stats.RequestsFailed, 1)
						if opts.Proxy != "" && (strings.Contains(reqErr.Error(), "authentication") || strings.Contains(reqErr.Error(), "username/password")) {
							atomic.AddInt64(&stats.FailedProxyAuth, 1)
						}
						if urlErr, ok := reqErr.(*url.Error); ok && urlErr.Timeout() {
							atomic.AddInt64(&stats.FailedTimeout, 1)
						}
						continue
					}

					errWhileReading := false
					bodyReadStartTime := time.Now()
					for {
						slice := make([]byte, 128*1024) // Consider making buffer size configurable or smaller
						n, readErr := resp.Body.Read(slice)
						atomic.AddInt64(&stats.BytesTransferred, int64(n))
						if readErr == io.EOF {
							break
						} else if readErr != nil {
							// Check for timeout on read operation specifically
							if opts.Timeout > 0 && time.Since(bodyReadStartTime) > time.Duration(opts.Timeout)*time.Second {
								errCollector.Add(fmt.Errorf("timeout while reading response body: %w", readErr))
								atomic.AddInt64(&stats.FailedTimeout, 1)
							} else {
								errCollector.Add(readErr)
							}
							atomic.AddInt64(&stats.RequestsFailed, 1)
							if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() { // This might be redundant if bodyReadStartTime check is robust
								atomic.AddInt64(&stats.FailedTimeout, 1)
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
						atomic.AddInt64(&stats.RequestsCompletedCode[0], 1) // For out-of-range status codes
					}
				}
			}()
		}
		burstWg.Wait()
		if b+1 < opts.Bursts && stopChan != nil {
			time.Sleep(time.Second * time.Duration(opts.Delay))
		}
	}

	bar.Finish()
	stats.EndTime = time.Now()
	
	// Ensure progress bar goroutine finishes by waiting for it after benchmark loops.
	// It will exit once stopChan is nil (or closed) and all requests are processed.
	// If stopChan was signaled, it might take a moment for all inflight requests to complete/fail.
	// We need to make sure the progress bar updater sees the final counts.
	// One way is to signal it to stop and then wait.
	// However, the current logic relies on stopChan becoming nil *or* totalRequests being reached.
	// A short wait after bar.Finish() might be needed for the progress bar goroutine to see the final state.
	// Or, more robustly, explicitly signal and wait for the progress bar goroutine.
	// For now, let's assume it catches up due to bar.Finish() and the subsequent small delay before main exits.
	// A better approach would be to close a dedicated channel for the progress bar go routine.
	progressWg.Wait()


	return stats, nil
}

func main() {
	// Subscribe to signals
	signal.Notify(mainStopChan, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)

	// Handle stop signal for graceful shutdown
	go func() {
		sig := <-mainStopChan // Wait for a signal
		if sig != nil {       // If a signal is received (not channel close)
			fmt.Printf("\nStopping benchmark...\n\n")
			// Make mainStopChan nil to signal benchmark loops and progress bar to stop
			// This is a bit of a hack. A dedicated channel for signalling stop to runBenchmark would be cleaner.
			// Or pass a context.
			tmpChan := mainStopChan
			mainStopChan = nil 
			close(tmpChan) // Close it to unblock any other listeners if any, and to make it unusable for further signals.
			signal.Reset(syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM) // Allow immediate exit on second signal
		}
	}()

	parser := flags.NewParser(&mainOpts, flags.HelpFlag)
	_, err := parser.Parse()

	if mainOpts.Version {
		fmt.Println(version)
		os.Exit(0)
	}

	if err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			// normal help behaviour
			parser.WriteHelp(os.Stdout)
			os.Exit(0) // Help should exit with 0
		} else {
			fmt.Println("Usage error:", err)
			fmt.Println()
			parser.WriteHelp(os.Stdout)
			os.Exit(1)
		}
	}
	
	errCollector := NewErrorCollector(10000, maxUniqueErrors, mainOpts.ShowErrors)
	errCollector.Start()

	finalStats, runErr := runBenchmark(mainOpts, mainStopChan, errCollector)

	errCollector.Stop() // Ensure all errors are processed

	if runErr != nil {
		// runBenchmark encountered a fatal setup error
		exitWithErrorMsg(1, "Benchmark setup failed: %s", runErr) // Use a generic exit code for runBenchmark failures
	}

	// If runBenchmark returned stats, print them
	if finalStats != nil {
		elapsed := finalStats.EndTime.Sub(finalStats.StartTime)
		fmt.Printf("Number of bursts:             %d\n", mainOpts.Bursts)
		fmt.Printf("Number of request per burst   %d\n", mainOpts.Requests)
		fmt.Printf("Concurrency level:            %d\n", mainOpts.Concurrency)
		fmt.Printf("Time taken for tests:         %s\n\n", elapsed)

		fmt.Printf("Total initiated requests:     %d\n", finalStats.Requests)
		fmt.Printf("   Completed requests:        %d\n", finalStats.RequestsCompleted)
		for c := range finalStats.RequestsCompletedCode {
			if finalStats.RequestsCompletedCode[c] > 0 {
				fmt.Printf("      HTTP-%03d completed:     %d\n", c, finalStats.RequestsCompletedCode[c])
			}
		}

		fmt.Printf("   Failed requests:           %d\n", finalStats.RequestsFailed)
		if finalStats.FailedProxyAuth > 0 {
			fmt.Printf("      Proxy auth failures:    %d\n", finalStats.FailedProxyAuth)
		}
		if finalStats.FailedTimeout > 0 {
			fmt.Printf("      Timeout failures:       %d\n", finalStats.FailedTimeout)
		}
		fmt.Printf("\nTotal transferred:            %d bytes\n", finalStats.BytesTransferred)

		if finalStats.Requests > 0 && elapsed > 0 { // Avoid division by zero
			// Calculate timePerReq as float64 for precision before converting to duration
			timePerReqNanos := float64(elapsed.Nanoseconds()) / float64(finalStats.Requests)
			timePerReq := time.Duration(timePerReqNanos)
			
			reqPerSec := float64(finalStats.Requests) / elapsed.Seconds()
			fmt.Printf("Requests per second:          %.3f\n", reqPerSec)
			fmt.Printf("Time per request:             %s\n", timePerReq)
		} else if finalStats.Requests > 0 { // If elapsed is zero but requests exist (very fast)
            fmt.Printf("Requests per second:          N/A (elapsed time is zero)\n")
            fmt.Printf("Time per request:             0s\n")
        }
	}

	// Print errors if any
	sortedErrors := errCollector.GetSortedErrors()
	if len(sortedErrors) > 0 {
		fmt.Printf("\nErrors:\n")
		// Ensure errList is not empty before accessing errList[0]
		errPrintfFormat := "% " + strconv.Itoa(len(strconv.Itoa(sortedErrors[0].cnt))+1) + "dx  %s\n"
		for idx, item := range sortedErrors {
			if idx >= errCollector.maxUniqueErrors { // Use maxUniqueErrors from collector
				fmt.Printf("... (list truncated)\n")
				break
			}
			fmt.Printf(errPrintfFormat, item.cnt, item.errMsg)
		}
	}
	
	if mainStopChan == nil && finalStats != nil && finalStats.Requests > 0 && (finalStats.Requests != (finalStats.RequestsCompleted + finalStats.RequestsFailed)) {
		// This condition suggests an early exit due to signal, where not all requests were accounted for as completed or failed by the time stats were captured.
		// Or if stopChan became nil (signalled) and total requests not reached.
		os.Exit(130) // Common exit code for SIGINT
	}

	// Determine final exit code based on whether there were request failures, if not already exited.
	if finalStats != nil && finalStats.RequestsFailed > 0 {
		os.Exit(1) // Exit with 1 if there were any failed requests during the benchmark
	}

	os.Exit(0) // Success
}
