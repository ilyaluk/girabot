package retryablehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/hasura/go-graphql-client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

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

var (
	requestTimeout = 5 * time.Second
	retryCount     = 10
)

func SetRequestTimeout(timeout time.Duration) {
	// hacky way to do this, but eh
	requestTimeout = timeout
}

func SetRetryCount(count int) {
	retryCount = count
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	requestsCnt.Inc()

	req.Header.Set("User-Agent", "Gira/3.4.3 (Android 34)")

	// Clone the request body
	var reqBytes []byte
	var err error
	if req.Body != nil {
		reqBytes, err = io.ReadAll(req.Body)
		if err != nil {
			log.Printf("retry: error reading body: %s", err)
			return nil, err
		}
	}
	log.Println("retry: req:", req.Method, req.URL, string(reqBytes)[:min(len(reqBytes), 500)])

	var resp *http.Response

	for i := 0; i < retryCount; i++ {
		if req.Body != nil {
			req.Body = io.NopCloser(bytes.NewBuffer(reqBytes))
		}

		// limit the request time, then retry if it times out
		ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
		defer cancel()
		req := req.WithContext(ctx)

		sentRequestsCnt.Inc()
		resp, err = t.inner.RoundTrip(req)
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("retry: num %d, request timed out(%v): %s", i, requestTimeout, err)
			timeoutsCnt.Inc()
			continue
		}
		if err != nil {
			break
		}

		var respBytes []byte
		respBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			break
		}

		log.Println("retry: num", i, "resp:", resp.StatusCode, string(respBytes[:min(len(respBytes), 200)]))

		resp.Body = io.NopCloser(bytes.NewBuffer(respBytes))

		if !doRetry(resp, respBytes) {
			break
		}

		if i < retryCount-1 {
			retriesCnt.Inc()
			time.Sleep(backoff(i))
		}
	}

	return resp, err
}

func doRetry(resp *http.Response, respBytes []byte) bool {
	// if we got 5xx, retry
	if resp.StatusCode/100 == 5 {
		return true
	}

	// sometimes backend returns transient GraphQL errors, retry them
	if IsRetryableGraphQLError(respBytes) {
		return true
	}

	// otherwise, don't retry
	return false
}

// retryableGraphQLCodes are backend error codes that are transient and worth
// retrying: INVALID_OPERATION is regularly returned spuriously, while
// REDIS_CONNECTION / TYPE_INITIALIZATION indicate transient backend infra
// failures (returned as a 400).
var retryableGraphQLCodes = map[string]bool{
	"INVALID_OPERATION":   true,
	"REDIS_CONNECTION":    true,
	"TYPE_INITIALIZATION": true,
}

func IsRetryableGraphQLError(respBytes []byte) bool {
	var rv struct {
		Errors graphql.Errors
	}

	// if we can't decode response as expected error, don't retry
	if err := json.NewDecoder(bytes.NewBuffer(respBytes)).Decode(&rv); err != nil {
		log.Printf("retry: error decoding response: %s", err)
		return false
	}

	if len(rv.Errors) != 1 {
		return false
	}

	ext := rv.Errors[0].Extensions
	if ext == nil {
		return false
	}

	if code, ok := ext["code"].(string); ok && retryableGraphQLCodes[code] {
		return true
	}

	// Jesus, sometimes it's an array
	if codes, ok := ext["codes"].([]any); ok {
		for _, c := range codes {
			if code, ok := c.(string); ok && retryableGraphQLCodes[code] {
				return true
			}
		}
	}

	return false
}

func backoff(retries int) time.Duration {
	// 1.5^x / 2
	// 10 retries: ~56s
	return time.Duration(math.Pow(1.5, float64(retries))) * time.Second / 2
}
