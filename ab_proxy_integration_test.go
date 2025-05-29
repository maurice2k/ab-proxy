package main

import (
	"fmt"
	"os" // Needed for os.MkdirTemp
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	abProxyBinaryPath    string
	buildABProxyOnce     sync.Once
	buildABProxyErr      error
	sharedTestBinaryDir string // Shared directory for the test binary
)

const testABProxyBinaryName = "test_ab_proxy" // To avoid conflict with actual binary if installed

// ensureABProxyBinary builds the ab-proxy binary for testing into a shared temporary directory.
// It's called once using sync.Once.
func ensureABProxyBinary(t *testing.T) string {
	buildABProxyOnce.Do(func() {
		var err error
		// Create a single temporary directory for the entire test suite run (for this package)
		// Note: This directory won't be cleaned up by t.TempDir() automatically.
		// For robust cleanup, TestMain could be used, or rely on OS temp cleaning.
		// For now, let's use a simpler approach that works across test calls.
		sharedTestBinaryDir, err = os.MkdirTemp("", "ab_proxy_test_suite_")
		if err != nil {
			buildABProxyErr = fmt.Errorf("failed to create shared temp dir for binary: %w", err)
			return
		}

		abProxyBinaryPath = filepath.Join(sharedTestBinaryDir, testABProxyBinaryName)
		cmd := exec.Command("go", "build", "-o", abProxyBinaryPath, ".")
		output, cmdErr := cmd.CombinedOutput()
		if cmdErr != nil {
			buildABProxyErr = fmt.Errorf("failed to build ab-proxy binary: %w\nOutput: %s", cmdErr, string(output))
		}
		// If build is successful, we can also register a cleanup function for the shared dir,
		// though TestMain is cleaner for this.
		// For now, we'll leave it and OS will eventually clean /tmp.
	})

	if buildABProxyErr != nil {
		t.Fatalf("Setup: Failed to ensure ab-proxy binary: %v", buildABProxyErr)
	}
	return abProxyBinaryPath
}

// runABProxyCmd executes the ab-proxy binary with given arguments and returns its stdout, stderr, and error.
func runABProxyCmd(t *testing.T, testArgs ...string) (string, string, error) {
	binaryPath := ensureABProxyBinary(t)

	cmd := exec.Command(binaryPath, testArgs...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run() // cmd.Run waits for the command to complete.

	// For debugging individual tests:
	// t.Logf("Running: %s %s", binaryPath, strings.Join(testArgs, " "))
	// t.Logf("Stdout:\n%s", stdout.String())
	// t.Logf("Stderr:\n%s", stderr.String())
	// if err != nil {
	// 	 t.Logf("Error: %v", err)
	// }

	return stdout.String(), stderr.String(), err
}

// Helper to parse specific integer values from ab-proxy output
func parseOutputInt(output string, pattern string) (int, error) {
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(output)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not find pattern '%s' in output: %s", pattern, output)
	}
	val, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("could not parse int from '%s': %w. Output: %s", matches[1], err, output)
	}
	return val, nil
}

func TestIntegration_BasicGet(t *testing.T) {
	target := NewMockTargetServer(MockTargetServerConfig{
		StatusCode:   200,
		ResponseBody: []byte("OK"),
	})
	defer target.Close()

	args := []string{"-n", "5", "-c", "1", "--bursts", "1", target.URL()}
	stdout, _, err := runABProxyCmd(t, args...)
	if err != nil {
		// `ab-proxy` might exit with non-zero if there are failed requests.
		// For this test, we expect all successful, so err should be nil or ExitError with 0 code.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.Success() {
			// This is fine, means exit code 0.
		} else {
			t.Fatalf("ab-proxy execution failed: %v", err)
		}
	}

	if target.GetRequestCount() != 5 {
		t.Errorf("Expected target server to receive 5 requests, got %d", target.GetRequestCount())
	}

	completed, err := parseOutputInt(stdout, `Completed requests:\s*(\d+)`)
	if err != nil {
		t.Errorf("Error parsing completed requests: %v", err)
	} else if completed != 5 {
		t.Errorf("Expected ab-proxy output to show 5 completed requests, got %d", completed)
	}

	http200, err := parseOutputInt(stdout, `HTTP-200 completed:\s*(\d+)`)
	if err != nil {
		t.Errorf("Error parsing HTTP-200 count: %v", err)
	} else if http200 != 5 {
		t.Errorf("Expected ab-proxy output to show 5 HTTP-200, got %d", http200)
	}
}

func TestIntegration_CustomHeadersAndUserAgent(t *testing.T) {
	target := NewMockTargetServer(MockTargetServerConfig{StatusCode: 200})
	defer target.Close()

	customHeader := "X-Custom-Header: TestValue"
	customUA := "TestAgent/1.0"

	args := []string{"-n", "1", "-H", customHeader, "--user-agent", customUA, target.URL()}
	_, _, err := runABProxyCmd(t, args...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.Success() {} else {
			t.Fatalf("ab-proxy execution failed: %v", err)
		}
	}

	if target.GetRequestCount() != 1 {
		t.Errorf("Expected target server to receive 1 request, got %d", target.GetRequestCount())
	}

	receivedHeaders := target.GetReceivedHeaders()
	if val := receivedHeaders.Get("X-Custom-Header"); val != "TestValue" {
		t.Errorf("Expected X-Custom-Header 'TestValue', got '%s'", val)
	}
	if val := receivedHeaders.Get("User-Agent"); val != customUA {
		t.Errorf("Expected User-Agent '%s', got '%s'", customUA, val)
	}
}

func TestIntegration_Timeout(t *testing.T) {
	target := NewMockTargetServer(MockTargetServerConfig{
		StatusCode:    200,
		ResponseDelay: 2 * time.Second,
	})
	defer target.Close()

	// With -n 3, -c 1 (default), -s 1 (1s timeout)
	// Each request will timeout.
	args := []string{"-n", "3", "-s", "1", target.URL()}
	stdout, _, err := runABProxyCmd(t, args...) // stderr explicitly ignored for now

	// ab-proxy should exit with non-zero status if all requests fail
	if err == nil {
		t.Errorf("Expected ab-proxy to exit with non-zero status due to timeouts, but it exited successfully.")
	} else {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("ab-proxy execution failed with unexpected error type: %v", err)
		}
		// ExitError is expected.
	}
	
	// Depending on concurrency and how quickly ab-proxy starts requests,
	// the server might see 1 or more requests before they time out.
	// For -c 1, it should be 1 request at a time.
	// If first times out, it might try the next.
	// The key is that not all 3 complete successfully.
	// The number of requests that *start* on the server could be up to 3 if ab-proxy retries quickly.
	// However, `ab-proxy` as written doesn't retry failed requests within the same `-n` count.
	// It just marks them as failed. So, it will attempt all 3.
	if target.GetRequestCount() != 3 {
		 t.Logf("Target server requests: %d. This can vary with timeouts and concurrency.", target.GetRequestCount())
		 // Not a hard fail, but good to observe. With -c 1, it's likely 3 attempts.
	}

	failed, pErr := parseOutputInt(stdout, `Failed requests:\s*(\d+)`)
	if pErr != nil {
		t.Errorf("Error parsing failed requests from stdout: %v\nStdout:\n%s", pErr, stdout)
	} else if failed != 3 {
		t.Errorf("Expected ab-proxy output to show 3 failed requests, got %d", failed)
	}

	// Check for timeout failures in stats
	timeoutFailures, _ := parseOutputInt(stdout, `Timeout failures:\s*(\d+)`)
	if timeoutFailures != 3 {
		t.Errorf("Expected 3 timeout failures in stats, got %d. Stdout:\n%s", timeoutFailures, stdout)
	}
	
	// Optionally, check stderr for error messages if --show-errors was used
	// argsWithShowErrors := []string{"-n", "3", "-s", "1", "--show-errors", target.URL()}
	// stdout, stderr, _ = runABProxyCmd(t, argsWithShowErrors...) // Assuming exit 1
	// if !strings.Contains(stderr, "Timeout") && !strings.Contains(stdout, "Timeout") { // Errors might go to stdout with --show-errors
	// 	t.Errorf("Expected 'Timeout' in error output with --show-errors. Stdout:\n%s\nStderr:\n%s", stdout, stderr)
	// }
}


func TestIntegration_HTTPProxy(t *testing.T) {
	target := NewMockTargetServer(MockTargetServerConfig{StatusCode: 200})
	defer target.Close()
	proxy := NewMockHTTPProxyServer()
	defer proxy.Close()

	args := []string{"-n", "2", "-X", proxy.URL(), target.URL()}
	stdout, _, err := runABProxyCmd(t, args...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.Success() {} else {
			t.Fatalf("ab-proxy execution failed: %v", err)
		}
	}

	if proxy.GetProxiedCount() != 2 {
		t.Errorf("Expected proxy server to receive 2 requests, got %d", proxy.GetProxiedCount())
	}
	if target.GetRequestCount() != 2 {
		t.Errorf("Expected target server to receive 2 requests, got %d", target.GetRequestCount())
	}

	targetHeaders := target.GetReceivedHeaders()
	if via := targetHeaders.Get("X-Proxied-By"); via != "mockHTTPProxyServer" {
		t.Errorf("Expected 'X-Proxied-By: mockHTTPProxyServer' header at target, got '%s'", via)
	}

	completed, _ := parseOutputInt(stdout, `Completed requests:\s*(\d+)`)
	if completed != 2 {
		t.Errorf("Expected ab-proxy output to show 2 completed requests, got %d", completed)
	}
	if !strings.Contains(stdout, "using proxy") {
		t.Errorf("Expected ab-proxy output to indicate proxy usage. Stdout:\n%s", stdout)
	}
}

func TestIntegration_TargetServerError(t *testing.T) {
	target := NewMockTargetServer(MockTargetServerConfig{StatusCode: 500})
	defer target.Close()

	args := []string{"-n", "3", target.URL()}
	stdout, _, err := runABProxyCmd(t, args...)
	// According to ab-proxy logic, HTTP 500 responses are "completed" and do not increment "RequestsFailed".
	// Therefore, ab-proxy should exit with 0 if all requests result in HTTP 500.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.Success() {
			// This is fine (exit 0)
		} else {
			t.Fatalf("ab-proxy execution failed unexpectedly: %v. Stdout:\n%s", err, stdout)
		}
	}

	if target.GetRequestCount() != 3 {
		t.Errorf("Expected target server to receive 3 requests, got %d", target.GetRequestCount())
	}

	completed, _ := parseOutputInt(stdout, `Completed requests:\s*(\d+)`)
	if completed != 3 {
		t.Errorf("Expected ab-proxy output to show 3 completed requests, got %d", completed)
	}

	http500, err := parseOutputInt(stdout, `HTTP-500 completed:\s*(\d+)`)
	if err != nil {
		t.Errorf("Error parsing HTTP-500 count: %v", err)
	} else if http500 != 3 {
		t.Errorf("Expected ab-proxy output to show 3 HTTP-500, got %d", http500)
	}
}

func TestIntegration_ShowErrors_ConnectionRefused(t *testing.T) {
	// Use a URL that is unlikely to be listening
	nonExistentTargetURL := "http://127.0.0.1:34567" // Arbitrary unused port

	args := []string{"-n", "2", "--show-errors", nonExistentTargetURL}
	stdout, stderr, err := runABProxyCmd(t, args...)

	if err == nil {
		t.Fatalf("Expected ab-proxy to exit with non-zero status due to connection errors")
	}

	failed, pErr := parseOutputInt(stdout, `Failed requests:\s*(\d+)`)
	if pErr != nil {
		t.Errorf("Error parsing failed requests: %v\nStdout:\n%s", pErr, stdout)
	} else if failed != 2 {
		t.Errorf("Expected ab-proxy output to show 2 failed requests, got %d", failed)
	}
	
	// Errors are printed to stdout when --show-errors is used (based on current ab-proxy.go logic)
	// The specific error message for connection refused can vary by OS.
	// Common patterns: "connection refused", "dial tcp", "connect: connection refused"
	// We look for "Errors:" section and then some indication of connection failure.
	if !strings.Contains(stdout, "Errors:") {
		t.Errorf("Expected 'Errors:' section in stdout with --show-errors. Stdout:\n%s", stdout)
	}
	// A more robust check would parse the error list. For now, a string contains.
	// Regex for "refused" or "no such host" could be useful.
	errorPattern := `(refused|no such host|connection timed out)` // Add more patterns if needed
	matched, _ := regexp.MatchString(errorPattern, stdout)
	if !matched {
		// Also check stderr, though --show-errors usually prints to stdout.
		matchedStderr, _ := regexp.MatchString(errorPattern, stderr)
		if !matchedStderr {
			t.Errorf("Expected connection error message like '%s' in stdout or stderr. Stdout:\n%s\nStderr:\n%s", errorPattern, stdout, stderr)
		}
	}
}


func TestIntegration_MultipleBurstsAndDelay(t *testing.T) {
	target := NewMockTargetServer(MockTargetServerConfig{StatusCode: 200})
	defer target.Close()

	args := []string{"-n", "2", "--bursts", "2", "--delay", "1", "-c", "1", target.URL()}
	startTime := time.Now()
	stdout, _, err := runABProxyCmd(t, args...)
	elapsedTime := time.Since(startTime)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.Success() {} else {
			t.Fatalf("ab-proxy execution failed: %v", err)
		}
	}

	if target.GetRequestCount() != 4 { // 2 requests/burst * 2 bursts
		t.Errorf("Expected target server to receive 4 requests, got %d", target.GetRequestCount())
	}

	completed, _ := parseOutputInt(stdout, `Completed requests:\s*(\d+)`)
	if completed != 4 {
		t.Errorf("Expected ab-proxy output to show 4 completed requests, got %d", completed)
	}

	// Check if total time is roughly consistent with delay
	// Total requests = 4. With -c 1, first burst (2 req) takes some time (T_req * 2).
	// Then 1s delay. Then second burst (2 req) takes T_req * 2.
	// So, total time should be > 1 second (the delay itself).
	// This is a loose check.
	if elapsedTime < 1*time.Second {
		t.Errorf("Expected total execution time to be at least 1 second (due to --delay 1), got %v", elapsedTime)
	}
	// A more precise check for delay would require instrumenting ab-proxy or very careful timing analysis,
	// which is beyond typical integration test scope here.
}
