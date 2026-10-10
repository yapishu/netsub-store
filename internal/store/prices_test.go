package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMarketRateCacheAndFailure(t *testing.T) {
	var calls atomic.Int32
	var bad atomic.Bool
	m := &MarketRates{key: "test-key", http: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("x-cg-demo-api-key") != "test-key" || r.URL.Query().Get("ids") != "bitcoin,ethereum,monero" {
			t.Error("missing batched query or authentication")
		}
		if bad.Load() {
			return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		body := fmt.Sprintf(`{"bitcoin":{"usd":100000,"last_updated_at":%d},"ethereum":{"usd":2500,"last_updated_at":%d},"monero":{"usd":200,"last_updated_at":%d}}`, time.Now().Unix(), time.Now().Unix(), time.Now().Unix())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rate, err := m.usdRate(context.Background(), "ETH")
			if err != nil || rate != "2500" {
				t.Error(rate, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("cache does not coalesce requests", calls.Load())
	}
	if v, _ := m.usdRate(context.Background(), "BTC"); v != "100000" {
		t.Fatal(v)
	}
	m.next = time.Now().Add(-time.Second)
	bad.Store(true)
	for i := 0; i < 3; i++ {
		if _, err := m.usdRate(context.Background(), "ETH"); err == nil {
			t.Fatal("stale quote used after provider failure")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("failed requests not throttled", calls.Load())
	}
}

func TestMarketRatesRejectInvalidOrStalePrices(t *testing.T) {
	for _, v := range []string{`{"usd":0}`, `{"usd":-1}`, `{"usd":2500,"last_updated_at":1}`, `{"usd":"invalid"}`, `null`} {
		m := &MarketRates{http: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ethereum":` + v + `}`))}, nil
		})}}
		if _, err := m.usdRate(context.Background(), "ETH"); err == nil {
			t.Fatal("invalid rate accepted", v)
		}
	}
}

func TestETHQuoteRoundsUpAndStaysFixed(t *testing.T) {
	if quote, err := coinQuote("1", "3", 18); err != nil || quote != "333333333333333334" {
		t.Fatal(quote, err)
	}
	s := testServer(t)
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":8453,"asset":"ETH"}`, "buyer")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var o Order
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	if o.Amount != "4000000000000000" || o.ETHRate != "2500" {
		t.Fatal(o.Amount, o.ETHRate)
	}
	s.rates.rates["ETH"] = "5000"
	if saved := readOrder(t, s, o.ID); saved.Amount != o.Amount || saved.ETHRate != o.ETHRate {
		t.Fatal("order quote changed")
	}
}
