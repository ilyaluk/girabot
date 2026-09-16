package retryablehttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRoundTripReadsLargeBodies(t *testing.T) {
	const size = 2 << 20
	body := strings.Repeat("x", size)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewTransport(nil)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if len(got) != size {
		t.Errorf("read %d bytes, want %d", len(got), size)
	}
}

func TestRoundTripRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "nope")
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewTransport(nil)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(got) != "ok" || resp.StatusCode != http.StatusOK {
		t.Errorf("got %q (%s) after %d calls", got, resp.Status, calls.Load())
	}
}

func TestRoundTripReplaysRequestBody(t *testing.T) {
	var calls atomic.Int32
	seen := make(chan string, 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		seen <- string(got)
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewTransport(nil)}
	resp, err := client.Post(srv.URL, "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	close(seen)

	for got := range seen {
		if got != `{"a":1}` {
			t.Errorf("server saw body %q", got)
		}
	}
}
