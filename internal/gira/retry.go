package gira

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/hasura/go-graphql-client"
)

type retryableTransport struct {
	inner http.RoundTripper
}

const retryCount = 10

func (t *retryableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "girabot (https://t.me/BetterGiraBot)")

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
	log.Println("retry: req:", req.Method, req.URL, string(reqBytes))

	var resp *http.Response

	for i := 0; i < retryCount; i++ {
		if req.Body != nil {
			req.Body = io.NopCloser(bytes.NewBuffer(reqBytes))
		}

		resp, err = t.inner.RoundTrip(req)
		if err != nil {
			break
		}

		var respBytes []byte
		respBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			break
		}

		log.Println("retry: num", i, "resp:", resp.StatusCode, string(respBytes[:200]))

		resp.Body = io.NopCloser(bytes.NewBuffer(respBytes))

		if !doRetry(resp, respBytes) {
			break
		}

		if i < retryCount-1 {
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

	// sometimes backend just returns shitty INVALID_OPERATION error, retry it
	if isInvalidOperationError(respBytes) {
		return true
	}

	// otherwise, don't retry
	return false
}

func isInvalidOperationError(respBytes []byte) bool {
	var rv struct {
		Errors graphql.Errors
	}

	// if we can't decode response as expected error, don't retry
	if err := json.NewDecoder(bytes.NewBuffer(respBytes)).Decode(&rv); err != nil {
		log.Printf("retry: error decoding response: %s", err)
		return false
	}

	if len(rv.Errors) == 1 {
		if ext := rv.Errors[0].Extensions; ext != nil {
			code, ok := ext["code"].(string)
			if ok && code == "INVALID_OPERATION" {
				return true
			}
		}
	}

	return false
}

func backoff(retries int) time.Duration {
	// 1.3^x / 2
	// 10 retries: ~22s
	return time.Duration(math.Pow(1.3, float64(retries))) * time.Second / 2
}
