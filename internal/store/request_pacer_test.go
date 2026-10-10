package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIdleOrdersDoNotQueryPaymentProviders(t *testing.T) {
	s := testServer(t)
	calls := 0
	s.chain.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"result":null}`)), Header: make(http.Header)}, nil
	})}
	s.tick(context.Background())
	original := create(t, s, "~sampel-palnet")
	for _, status := range []string{"awaiting_payment", "expired", "review_payment", "delivered", "confirming"} {
		o := original
		o.Status = status
		o.EmailSent = true
		o.TxHash = ""
		if err := s.save(o); err != nil {
			t.Fatal(err)
		}
		s.tick(context.Background())
	}
	if calls != 0 {
		t.Fatalf("idle orders made %d provider requests", calls)
	}
	o := original
	o.Status = "confirming"
	o.TxHash = hash
	o.NextCheck = time.Now().Add(time.Hour)
	if err := s.save(o); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if calls != 0 {
		t.Fatal("order queried before its retry time")
	}
	o.NextCheck = time.Time{}
	if err := s.save(o); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if calls != 1 {
		t.Fatalf("submitted transaction made %d requests, want one pending receipt", calls)
	}
	s.tick(context.Background())
	if calls != 1 {
		t.Fatal("pending receipt ignored retry backoff")
	}
}

func TestNockblocksRequestsShareFiveSecondLimit(t *testing.T) {
	s, _ := nockTestServer(t)
	s.nock.requestInterval = 5 * time.Second
	var mu sync.Mutex
	starts := []time.Time{}
	transport := s.nock.http.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	s.nock.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return transport.RoundTrip(r)
	})
	errs := make(chan error, 2)
	for _, price := range []bool{true, false} {
		go func(price bool) {
			var out json.RawMessage
			_, err := s.nock.call(context.Background(), price, "getTip", nil, &out)
			errs <- err
		}(price)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	elapsed := starts[1].Sub(starts[0])
	mu.Unlock()
	if elapsed < 5*time.Second-10*time.Millisecond {
		t.Fatalf("Nockblocks requests only %s apart", elapsed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var out json.RawMessage
	if _, err := s.nock.call(ctx, false, "getTip", nil, &out); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting request did not cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 {
		t.Fatal("canceled request reached Nockblocks")
	}
}

func TestEVMRequestsShareLimitAcrossChains(t *testing.T) {
	var mu sync.Mutex
	starts := []time.Time{}
	reader := ChainReader{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"result":"0x1"}`)), Header: make(http.Header)}, nil
	})}}
	errs := make(chan error, 2)
	for _, id := range []int{1, 8453} {
		go func(id int) {
			var result string
			errs <- reader.call(context.Background(), Chain{ID: id}, "eth_blockNumber", nil, &result)
		}(id)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if starts[1].Sub(starts[0]) < time.Second-10*time.Millisecond {
		t.Fatal("chain requests exceeded shared limit")
	}
}

func TestEtherscanRateLimitDefersOtherOrders(t *testing.T) {
	for _, body := range []string{"http", `{"status":"0","message":"NOTOK","result":"Max rate limit reached"}`} {
		t.Run(body[:4], func(t *testing.T) {
			calls := 0
			reader := ChainReader{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				status := 200
				if body == "http" {
					status = 429
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}
			var result string
			if err := reader.call(context.Background(), Chain{ID: 1}, "eth_blockNumber", nil, &result); err == nil {
				t.Fatal("rate limit accepted")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := reader.call(ctx, Chain{ID: 8453}, "eth_blockNumber", nil, &result); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("rate-limited request was not deferred")
			}
			if calls != 1 {
				t.Fatal("provider called during cooldown")
			}
		})
	}
}
