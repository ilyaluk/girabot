// Package retryablehttp provides an HTTP transport that retries requests the
// Gira backend is known to fail transiently.
package retryablehttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// UserAgent is sent with every request, matching the official app.
const UserAgent = "Gira/3.4.3 (Android 34)"

type Transport struct {
	inner http.RoundTripper
}

func NewTransport(inner http.RoundTripper) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &Transport{inner: inner}
}

var (
	requestsCnt     = promauto.NewCounter(prometheus.CounterOpts{Name: "gira_requests_total"})
	sentRequestsCnt = promauto.NewCounter(prometheus.CounterOpts{Name: "gira_sent_requests_total"})
	timeoutsCnt     = promauto.NewCounter(prometheus.CounterOpts{Name: "gira_timeout_retries_total"})
	retriesCnt      = promauto.NewCounter(prometheus.CounterOpts{Name: "gira_retries_total"})
)

const (
	requestTimeout = 10 * time.Second
	retryCount     = 5
)

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	requestsCnt.Inc()

	req.Header.Set("User-Agent", UserAgent)

	// Clone the request body so a retry can send it again.
	var reqBytes []byte
	var err error
	if req.Body != nil {
		reqBytes, err = io.ReadAll(req.Body)
		if err != nil {
			log.Printf("retry: error reading body: %s", err)
			return nil, err
		}
	}
	// Bodies are never logged: a login request carries the user's password and
	// most answers carry a token.
	log.Println("retry: req:", req.Method, req.URL.Host, req.URL.Path, len(reqBytes), "bytes")

	var resp *http.Response

	for i := 0; i < retryCount; i++ {
		if req.Body != nil {
			req.Body = io.NopCloser(bytes.NewBuffer(reqBytes))
		}

		// limit the request time, then retry if it times out
		ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
		attempt := req.WithContext(ctx)

		sentRequestsCnt.Inc()
		resp, err = t.inner.RoundTrip(attempt)
		if err == nil {
			// The per-attempt deadline dies with this iteration, so the body has
			// to be drained into memory before the caller ever reads it.
			var respBytes []byte
			respBytes, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewBuffer(respBytes))
		}
		cancel()

		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("retry: num %d, request timed out(%v): %s", i, requestTimeout, err)
			timeoutsCnt.Inc()
			continue
		}
		if err != nil {
			break
		}

		log.Println("retry: num", i, "resp:", resp.StatusCode)

		if !doRetry(resp) {
			break
		}

		if i < retryCount-1 {
			retriesCnt.Inc()
			time.Sleep(backoff(i))
		}
	}

	if err != nil {
		return nil, err
	}
	return resp, nil
}

func doRetry(resp *http.Response) bool {
	// if we got 5xx, retry
	return resp.StatusCode/100 == 5
}

func backoff(retries int) time.Duration {
	// 1.5^x / 2
	return time.Duration(math.Pow(1.5, float64(retries))) * time.Second / 2
}
