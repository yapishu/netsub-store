package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"sync"
	"time"
)

var marketRatePattern = regexp.MustCompile(`^[0-9]{1,20}(\.[0-9]{1,20})?([eE][+-]?[0-9]{1,2})?$`)

// MarketRates batches currencies and fetches at most once per minute, on demand.
type MarketRates struct {
	mu    sync.Mutex
	http  *http.Client
	key   string
	rates map[string]string
	next  time.Time
}

func (m *MarketRates) usdRate(ctx context.Context, asset string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unavailable := errors.New("Market price unavailable; try again in a minute")
	if time.Now().Before(m.next) {
		if rate := m.rates[asset]; rate != "" {
			return rate, nil
		}
		return "", unavailable
	}
	m.next = time.Now().Add(time.Minute)
	m.rates = nil
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin,ethereum,monero&vs_currencies=usd&include_last_updated_at=true", nil)
	if err != nil {
		return "", unavailable
	}
	if m.key != "" {
		req.Header.Set("x-cg-demo-api-key", m.key)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return "", unavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", unavailable
	}
	var values map[string]struct {
		USD     json.Number `json:"usd"`
		Updated int64       `json:"last_updated_at"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&values) != nil {
		return "", unavailable
	}
	m.rates = make(map[string]string)
	for symbol, id := range map[string]string{"BTC": "bitcoin", "ETH": "ethereum", "XMR": "monero"} {
		v := values[id]
		if len(v.USD) > 40 || !marketRatePattern.MatchString(string(v.USD)) {
			continue
		}
		rate, ok := new(big.Rat).SetString(string(v.USD))
		if !ok || rate.Sign() <= 0 || len(v.USD) > 40 || v.Updated < time.Now().Add(-10*time.Minute).Unix() || v.Updated > time.Now().Add(time.Minute).Unix() {
			continue
		}
		m.rates[symbol] = string(v.USD)
	}
	if rate := m.rates[asset]; rate != "" {
		return rate, nil
	}
	return "", unavailable
}

func coinQuote(usd, rate string, decimals int) (string, error) {
	dollars, err := units(usd, 6)
	if err != nil {
		return "", err
	}
	if len(rate) > 40 || !marketRatePattern.MatchString(rate) {
		return "", errors.New("Invalid exchange rate")
	}
	price, ok := new(big.Rat).SetString(rate)
	if !ok || price.Sign() <= 0 {
		return "", errors.New("Invalid exchange rate")
	}
	value := new(big.Rat).SetFrac(new(big.Int).Mul(dollars, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)), big.NewInt(1_000_000))
	value.Quo(value, price)
	amount, remainder := new(big.Int), new(big.Int)
	amount.QuoRem(value.Num(), value.Denom(), remainder)
	if remainder.Sign() > 0 {
		amount.Add(amount, big.NewInt(1))
	}
	if amount.Sign() <= 0 || amount.BitLen() > 256 {
		return "", errors.New("Quote is out of range")
	}
	return amount.String(), nil
}
