package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
	"strings" // Added for strings.HasPrefix
)

// --- Mock HTTP Target Server ---

type MockTargetServerConfig struct {
	StatusCode   int
	ResponseBody []byte
	ResponseDelay time.Duration
	// Add any other configurations like specific headers to check for, etc.
}

type MockTargetServer struct {
	Server           *httptest.Server
	Config           MockTargetServerConfig
	mu               sync.Mutex
	ReceivedHeaders  http.Header
	RequestCount     int64
	LastRequestBody  []byte
	ReceivedRequests []*http.Request // Store all received requests for detailed inspection
}

func NewMockTargetServer(config MockTargetServerConfig) *MockTargetServer {
	mts := &MockTargetServer{
		Config:          config,
		ReceivedHeaders: make(http.Header),
	}

	mts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&mts.RequestCount, 1)

		// Capture headers
		mts.mu.Lock()
		for k, v := range r.Header {
			mts.ReceivedHeaders[k] = append(mts.ReceivedHeaders[k], v...)
		}
		// Store a copy of the request for potential later inspection
		var bodyBytes []byte
		var err error
		if r.Body != nil {
			bodyBytes, err = io.ReadAll(r.Body)
			if err == nil {
				mts.LastRequestBody = bodyBytes
			}
			r.Body.Close() // Important to close the body
		}
		mts.ReceivedRequests = append(mts.ReceivedRequests, r)
		mts.mu.Unlock()

		if mts.Config.ResponseDelay > 0 {
			time.Sleep(mts.Config.ResponseDelay)
		}

		w.WriteHeader(mts.Config.StatusCode)
		if mts.Config.ResponseBody != nil {
			w.Write(mts.Config.ResponseBody)
		}
	}))

	return mts
}

func (mts *MockTargetServer) URL() string {
	return mts.Server.URL
}

func (mts *MockTargetServer) Close() {
	mts.Server.Close()
}

func (mts *MockTargetServer) GetRequestCount() int64 {
	return atomic.LoadInt64(&mts.RequestCount)
}

func (mts *MockTargetServer) GetReceivedHeaders() http.Header {
	mts.mu.Lock()
	defer mts.mu.Unlock()
	hdrs := make(http.Header)
	for k, v := range mts.ReceivedHeaders {
		hdrs[k] = append([]string(nil), v...)
	}
	return hdrs
}

func (mts *MockTargetServer) GetLastRequestBody() []byte {
	mts.mu.Lock()
	defer mts.mu.Unlock()
	return mts.LastRequestBody 
}

func (mts *MockTargetServer) GetReceivedRequest(index int) *http.Request {
	mts.mu.Lock()
	defer mts.mu.Unlock()
	if index < 0 || index >= len(mts.ReceivedRequests) {
		return nil
	}
	return mts.ReceivedRequests[index]
}


// --- Mock HTTP Proxy Server ---

type MockHTTPProxyServer struct {
	Server            *httptest.Server
	ProxiedCount      int64
	mu                sync.Mutex
	ReceivedViaHeader bool 
	TargetDialErrors  int64 
}

func NewMockHTTPProxyServer() *MockHTTPProxyServer {
	mps := &MockHTTPProxyServer{}

	mps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			atomic.AddInt64(&mps.ProxiedCount, 1) 
			w.WriteHeader(http.StatusOK)
			return
		}

		targetURL := r.RequestURI
		if targetURL == "" { 
			http.Error(w, "missing request URI", http.StatusBadRequest)
			return
		}
		
		var body io.Reader = r.Body
		outReq, err := http.NewRequest(r.Method, targetURL, body)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create request to target: %v", err), http.StatusInternalServerError)
			atomic.AddInt64(&mps.TargetDialErrors, 1)
			return
		}

		outReq.Header = make(http.Header)
		for key, values := range r.Header {
			if strings.HasPrefix(strings.ToLower(key), "proxy-") {
				continue
			}
			outReq.Header[key] = values
		}
		outReq.Header.Set("X-Proxied-By", "mockHTTPProxyServer")

		client := &http.Client{
			Transport: &http.Transport{Proxy: nil},
		} 
		
		resp, err := client.Do(outReq)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to get response from target: %v", err), http.StatusBadGateway)
			atomic.AddInt64(&mps.TargetDialErrors, 1)
			return
		}
		defer resp.Body.Close()

		atomic.AddInt64(&mps.ProxiedCount, 1)

		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	return mps
}

func (mps *MockHTTPProxyServer) URL() string {
	return mps.Server.URL
}

func (mps *MockHTTPProxyServer) Close() {
	mps.Server.Close()
}

func (mps *MockHTTPProxyServer) GetProxiedCount() int64 {
	return atomic.LoadInt64(&mps.ProxiedCount)
}

func (mps *MockHTTPProxyServer) GetTargetDialErrors() int64 {
	return atomic.LoadInt64(&mps.TargetDialErrors)
}

// Note: A SOCKS5 mock server is significantly more complex.
// The example main function and detailed feature comments have been removed to prevent parsing issues.
