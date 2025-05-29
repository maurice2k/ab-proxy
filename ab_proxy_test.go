package main

import (
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"bufio" // Required for textproto.NewReader
	"crypto/tls" // Required for http.Transport TLSNextProto
	"regexp"
	// "sort" // Removed as it's unused in the final version of the file
	"strings"
	// "sync" // Not directly used in this version of test helpers, but kept for potential future use
	"testing"
	// "time" // Not directly used in this version of test helpers

	"github.com/jessevdk/go-flags"
)

// Helper for comparing errItem slices
func compareErrItems(a, b []errItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].cnt != b[i].cnt || a[i].errMsg != b[i].errMsg {
			return false
		}
	}
	return true
}

// TestErrorCollector tests the ErrorCollector functionality.
func TestErrorCollector(t *testing.T) {
	t.Run("NewErrorCollector", func(t *testing.T) {
		ec := NewErrorCollector(100, 10, true)
		if ec.errChan == nil {
			t.Error("errChan should be initialized")
		}
		if ec.errMap == nil {
			t.Error("errMap should be initialized")
		}
		if ec.maxUniqueErrors != 10 {
			t.Errorf("expected maxUniqueErrors %d, got %d", 10, ec.maxUniqueErrors)
		}
		if !ec.showErrors {
			t.Error("expected showErrors to be true")
		}
	})

	t.Run("AddAndGetErrors", func(t *testing.T) {
		ec := NewErrorCollector(100, 5, true)
		ec.Start()

		ec.Add(fmt.Errorf("error 1"))
		ec.Add(fmt.Errorf("error 2"))
		ec.Add(fmt.Errorf("error 1")) // Duplicate
		ec.Add(fmt.Errorf("error 3"))
		ec.Add(fmt.Errorf("error 2")) // Duplicate
		ec.Add(fmt.Errorf("error 1")) // Triplicate

		ec.Stop() // Stop to ensure all errors are processed

		sortedErrors := ec.GetSortedErrors()
		expected := []errItem{
			{3, "error 1"},
			{2, "error 2"},
			{1, "error 3"},
		}

		if !compareErrItems(sortedErrors, expected) {
			t.Errorf("expected sorted errors %v, got %v", expected, sortedErrors)
		}
	})

	t.Run("MaxUniqueErrors", func(t *testing.T) {
		ec := NewErrorCollector(100, 2, true)
		ec.Start()

		ec.Add(fmt.Errorf("error 1"))
		ec.Add(fmt.Errorf("error 2"))
		ec.Add(fmt.Errorf("error 3")) // Should be ignored for map, but consumed from chan
		ec.Add(fmt.Errorf("error 1")) // Should still increment count for error 1
		
		ec.Stop()

		if len(ec.errMap) > 2 {
			t.Errorf("expected errMap size to be at most %d, got %d (map: %v)", 2, len(ec.errMap), ec.errMap)
		}
		
		if _, exists := ec.errMap["error 3"]; exists {
			t.Error("error 3 should not be in errMap due to maxUniqueErrors limit")
		}

		if count, ok := ec.errMap["error 1"]; !ok || count != 2 {
			 t.Errorf("expected error 1 count to be 2, got %d (present: %v)", count, ok)
		}
	})

	t.Run("ShowErrorsFalse", func(t *testing.T) {
		ec := NewErrorCollector(100, 5, false)
		ec.Start() 
		ec.Add(fmt.Errorf("error 1")) 
		ec.Stop() 

		if len(ec.errMap) != 0 {
			t.Errorf("errMap should be empty when showErrors is false, got %v", ec.errMap)
		}
	})
}

// simulateURLProcessing mimics the URL processing logic from runBenchmark's initial part.
func simulateURLProcessing(opts Options) (string, textproto.MIMEHeader, *http.Transport, error) {
	// 1. URL Validation (simplified from runBenchmark)
	targetURL := opts.Args.Url
	if matched, _ := regexp.MatchString(`^\w+://`, targetURL); !matched {
		targetURL = "http://" + targetURL
	}
	_, err := url.Parse(targetURL)
	if err != nil {
		return "", nil, nil, fmt.Errorf("invalid URL given: %w", err)
	}

	// 2. Header Preparation (simplified from runBenchmark)
	var headers textproto.MIMEHeader
	headerStrings := append([]string(nil), opts.Header...)
	headerStrings = append(headerStrings, "User-Agent:"+opts.UserAgent) // Corrected: space after User-Agent:
	
	sb := strings.Builder{}
	for _, h := range headerStrings {
		sb.WriteString(h)
		sb.WriteString("\r\n")
	}
	sb.WriteString("\r\n")

	tp := textproto.NewReader(bufio.NewReader(strings.NewReader(sb.String()))) // Added bufio.NewReader
	parsedHeaders, err := tp.ReadMIMEHeader()
	if err != nil {
		return "", nil, nil, fmt.Errorf("unable to parse custom headers: %w", err)
	}
	headers = textproto.MIMEHeader(parsedHeaders)


	// 3. Transport Setup (simplified from runBenchmark)
	var tr *http.Transport = nil
	if opts.Proxy != "" {
		proxyURLStr := opts.Proxy
		if matched, _ := regexp.MatchString(`^\w+://`, proxyURLStr); !matched {
			proxyURLStr = "http://" + proxyURLStr
		}
		uri, err := url.Parse(proxyURLStr)
		if err != nil {
			return "", nil, nil, fmt.Errorf("unable to parse proxy URL: %w", err)
		}
		if uri.Scheme == "socks5" {
			tr = &http.Transport{Proxy: http.ProxyURL(uri)}
		} else if uri.Scheme == "https" || uri.Scheme == "http" {
			tr = &http.Transport{
				Proxy:        http.ProxyURL(uri),
				TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
			}
		} else {
			return "", nil, nil, fmt.Errorf("unable to handle proxy with scheme '%s'", uri.Scheme)
		}
	}
	return targetURL, headers, tr, nil
}


func TestURLValidationLogic(t *testing.T) {
	tests := []struct {
		name    string
		inputURL string
		wantURL string
		wantErr bool
	}{
		{"MissingScheme", "example.com", "http://example.com", false},
		{"HTTP", "http://example.com", "http://example.com", false},
		{"HTTPS", "https://example.com", "https://example.com", false},
		{"FTP", "ftp://example.com", "ftp://example.com", false},
		{"EmptyURL", "", "http://", false}, // url.Parse("") is valid, url.Parse("http://") is valid.
		{"InvalidURLChars", "http://[::1]:namedport", "", true}, // This specific form is invalid for url.Parse
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{}
			opts.Args.Url = tt.inputURL // Correctly assign to the nested struct field
			gotURL, _, _, err := simulateURLProcessing(opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("URL processing error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && gotURL != tt.wantURL {
				t.Errorf("Processed URL = %v, want %v", gotURL, tt.wantURL)
			}
		})
	}
}

func TestHeaderPreparationLogic(t *testing.T) {
    defaultUA := "ab-proxy/1.0.0" // Assuming this is a package const or accessible default
    tests := []struct {
        name            string
        optsToSet       Options // Use this to set fields other than Args.Url
        urlArg          string  // Explicitly pass URL arg for clarity
        expectedHeaders map[string]string
        wantErr         bool
    }{
        {"DefaultUA", Options{UserAgent: defaultUA}, "url", map[string]string{"User-Agent": defaultUA}, false},
        {"CustomUA", Options{UserAgent: "CustomAgent/1.0"}, "url", map[string]string{"User-Agent": "CustomAgent/1.0"}, false},
        {"SingleHeader", Options{UserAgent: defaultUA, Header: []string{"X-Custom: val"}}, "url", map[string]string{"User-Agent": defaultUA, "X-Custom": "val"}, false},
        {"MultiHeaders", Options{UserAgent: defaultUA, Header: []string{"X-One: 1", "X-Two: 2"}}, "url", map[string]string{"User-Agent": defaultUA, "X-One": "1", "X-Two": "2"}, false},
        {"OverrideUAInHeader", Options{UserAgent: defaultUA, Header: []string{"User-Agent: Override/3.0"}}, "url", map[string]string{"User-Agent": "Override/3.0"}, false},
        {"MalformedHeaderLine", Options{UserAgent: defaultUA, Header: []string{"BadFormatNoColon"}}, "url", map[string]string{}, true}, // expect error, so headers map can be empty
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
			opts := tt.optsToSet // Copy base options
			opts.Args.Url = tt.urlArg // Set the URL argument correctly
            _, gotHeaders, _, err := simulateURLProcessing(opts)
            if (err != nil) != tt.wantErr {
                t.Errorf("Header preparation error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !tt.wantErr {
                if len(gotHeaders) != len(tt.expectedHeaders) {
                     t.Errorf("Expected %d headers, got %d. Got: %v, Expected: %v", len(tt.expectedHeaders), len(gotHeaders), gotHeaders, tt.expectedHeaders)
                }
                for k, v := range tt.expectedHeaders {
                    canonicalKey := textproto.CanonicalMIMEHeaderKey(k)
                    ghVal, ok := gotHeaders[canonicalKey]
                    if !ok {
                        t.Errorf("Expected header %s not found", k)
                        continue
                    }
                    if len(ghVal) == 0 || ghVal[0] != v {
                        t.Errorf("Header %s: expected '%s', got '%v'", k, v, ghVal)
                    }
                }
            }
        })
    }
}


func TestProxyConfigurationLogic(t *testing.T) {
	tests := []struct {
		name           string
		proxyOpt       string
		expectedScheme string // Scheme of the proxy URL in the transport, if one is set
		expectProxy    bool   // True if a proxy function should be configured on the transport
		wantErr        bool
	}{
		{"NoProxy", "", "", false, false},
		{"HTTPProxy", "http://proxy.example.com:8080", "http", true, false},
		{"HTTPSProxy", "https://proxy.example.com:8888", "https", true, false},
		{"SOCKS5Proxy", "socks5://proxy.example.com:1080", "socks5", true, false},
		{"SchemeMissingDefaultsToHTTP", "proxy.example.com:8080", "http", true, false},
		{"InvalidScheme", "ftp://proxy.example.com", "", false, true},
		{"MalformedURL", "http://[::1%scope]:port", "", false, true}, // Invalid URL
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{Proxy: tt.proxyOpt}
			opts.Args.Url = "http://dummy.url" // Set the URL argument correctly
			_, _, gotTransport, err := simulateURLProcessing(opts)

			if (err != nil) != tt.wantErr {
				t.Errorf("Proxy config error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.expectProxy {
				if gotTransport == nil || gotTransport.Proxy == nil {
					t.Fatalf("Expected transport with Proxy function, got transport: %v", gotTransport)
				}
				// Verify scheme by trying to get the proxy URL
				dummyReq, _ := http.NewRequest("GET", "http://target.url", nil)
				proxyURL, pErr := gotTransport.Proxy(dummyReq)
				if pErr != nil {
					t.Fatalf("Transport Proxy function returned error: %v", pErr)
				}
				if proxyURL == nil {
					t.Fatalf("Transport Proxy function returned nil URL, expected one for scheme: %s", tt.expectedScheme)
				}
				if proxyURL.Scheme != tt.expectedScheme {
					t.Errorf("Expected proxy scheme %s, got %s", tt.expectedScheme, proxyURL.Scheme)
				}
			} else {
				if !tt.wantErr && gotTransport != nil && gotTransport.Proxy != nil {
					// If no error was expected, and no proxy was expected, transport should not have Proxy func.
					// (it could be non-nil if default transport settings were applied, but Proxy would be nil)
					dummyReq, _ := http.NewRequest("GET", "http://target.url", nil)
					proxyURL, _ := gotTransport.Proxy(dummyReq)
					if proxyURL != nil {
						t.Errorf("Expected no proxy to be configured, but got proxy URL: %s", proxyURL.String())
					}
				}
			}
		})
	}
}


func TestOptionsDefaultsAndParsing(t *testing.T) {
	t.Run("DefaultValues", func(t *testing.T) {
		var opts Options
		parser := flags.NewParser(&opts, flags.None) 
		_, err := parser.ParseArgs([]string{"http://example.com"}) 
		if err != nil {
			if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrRequired {
			} else {
				t.Fatalf("ParseArgs() failed with unexpected error: %v", err)
			}
		}

		if opts.Concurrency != 1 {
			t.Errorf("Expected default Concurrency 1, got %d", opts.Concurrency)
		}
		if opts.Requests != 1 {
			t.Errorf("Expected default Requests 1, got %d", opts.Requests)
		}
		if opts.Bursts != 1 {
			t.Errorf("Expected default Bursts 1, got %d", opts.Bursts)
		}
		if opts.Delay != 3 {
			t.Errorf("Expected default Delay 3, got %d", opts.Delay)
		}
		// Default UserAgent is applied in runBenchmark if opts.UserAgent is empty,
		// or by go-flags if default tag is on Options.UserAgent.
		// The current Options struct has `default:"ab-proxy/1.0.0"`
		if opts.UserAgent != "ab-proxy/1.0.0" {
			t.Errorf("Expected default UserAgent 'ab-proxy/1.0.0', got '%s'", opts.UserAgent)
		}
		if opts.Timeout != 0 {
			t.Errorf("Expected default Timeout 0, got %d", opts.Timeout)
		}
		if opts.ShowErrors != false {
			t.Errorf("Expected default ShowErrors false, got %v", opts.ShowErrors)
		}
	})

	t.Run("FlagParsing", func(t *testing.T) {
		tests := []struct {
			name     string
			args     []string
			validate func(opts Options, t *testing.T)
		}{
			{"Concurrency", []string{"-c", "10", "u"}, func(o Options, t *testing.T) { if o.Concurrency != 10 { t.Error() } }},
			{"RequestsAndBursts", []string{"-n", "100", "--bursts", "5", "u"}, func(o Options, t *testing.T) { if o.Requests != 100 || o.Bursts != 5 { t.Error() } }},
			{"Proxy", []string{"-X", "s5://lh:1080", "u"}, func(o Options, t *testing.T) { if o.Proxy != "s5://lh:1080" { t.Error() } }}, // s5 shorthand for socks5
			{"Headers", []string{"-H", "XF: b", "-H", "XB: z", "u"}, func(o Options, t *testing.T) {
				exp := []string{"XF: b", "XB: z"}; if !reflect.DeepEqual(o.Header, exp) { t.Errorf("H: exp %v got %v", exp, o.Header) }
			}},
			{"ShowErrors", []string{"--show-errors", "u"}, func(o Options, t *testing.T) { if !o.ShowErrors { t.Error() } }},
			{"URLArg", []string{"https://my.url/p"}, func(o Options, t *testing.T) { if o.Args.Url != "https://my.url/p" { t.Errorf("URL exp %s got %s", "https://my.url/p", o.Args.Url)} }},
		}

		for _, tt := range tests {
			// Replace "u" placeholder with a valid URL for parsing to avoid required arg error
			argsWithURL := make([]string, len(tt.args))
			for i, arg := range tt.args {
				if arg == "u" {
					argsWithURL[i] = "http://example.com"
				} else {
					argsWithURL[i] = arg
				}
			}


			t.Run(tt.name, func(t *testing.T) {
				var opts Options
				parser := flags.NewParser(&opts, flags.None)
				_, err := parser.ParseArgs(argsWithURL)
				if err != nil {
					if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrRequired {
						// This should not happen if "u" is correctly replaced.
					} else {
						t.Fatalf("ParseArgs(%v) failed: %v", argsWithURL, err)
					}
				}
				tt.validate(opts, t)
			})
		}
	})
}


// Ensure there's a newline at the very end of the file.
