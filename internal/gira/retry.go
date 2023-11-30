package gira

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"time"
)

type retryableTransport struct {
	inner http.RoundTripper
}

const retryCount = 7

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
		log.Println("retry: req:", req.Method, req.URL, string(reqBytes))
	}

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

	var rv struct {
		Errs []struct {
			Message string `json:"message"`
			Exts    struct {
				Codes []string `json:"codes"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	// if we can't decode response as expected error, don't retry
	if err := json.NewDecoder(bytes.NewBuffer(respBytes)).Decode(&rv); err != nil {
		log.Printf("retry: error decoding response: %s", err)
		return false
	}

	log.Printf("rv: %+v", rv)

	// sometimes backend just returns this shitty error, just retry it
	if len(rv.Errs) == 1 && len(rv.Errs[0].Exts.Codes) == 1 && rv.Errs[0].Exts.Codes[0] == "INVALID_OPERATION" {
		return true
	}

	// otherwise, don't retry
	return false

}

func backoff(retries int) time.Duration {
	return time.Duration(math.Pow(1.5, float64(retries))) * time.Second / 2
}
