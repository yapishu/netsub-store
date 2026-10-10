package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var ErrPending = errors.New("waiting for payment confirmation")
var ErrPayment = errors.New("payment does not match this order")

type ChainReader struct {
	HTTP  *http.Client
	pacer requestPacer
}

// The shared limiter bounds explorer/RPC traffic across all active checkouts.
func (r *ChainReader) call(ctx context.Context, ch Chain, method string, params []any, out any) error {
	var req *http.Request
	var err error
	if ch.RPC != "" {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, err = http.NewRequestWithContext(ctx, "POST", ch.RPC, bytes.NewReader(body))
	} else {
		q := url.Values{"chainid": {strconv.Itoa(ch.ID)}, "module": {"proxy"}, "action": {method}, "apikey": {ch.ScanKey}}
		switch method {
		case "eth_getTransactionReceipt", "eth_getTransactionByHash":
			q.Set("txhash", params[0].(string))
		case "eth_getBlockByNumber":
			q.Set("tag", params[0].(string))
			q.Set("boolean", "false")
		}
		req, err = http.NewRequestWithContext(ctx, "GET", "https://api.etherscan.io/v2/api?"+q.Encode(), nil)
	}
	if err != nil {
		return errors.New("invalid chain endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if err := r.pacer.wait(ctx, time.Second); err != nil {
		return err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return errors.New("chain provider unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		r.pacer.deferFor(time.Minute)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("chain provider HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
		Status string          `json:"status"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid chain response")
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" || envelope.Status == "0" {
		r.pacer.deferFor(time.Minute)
		return errors.New("chain provider rejected query; check API plan and limits")
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return ErrPending
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return errors.New("unexpected chain response")
	}
	return nil
}

type chainTx struct{ Hash, From, To, Value, BlockHash, BlockNumber, Input string }
type chainLog struct {
	Address, Data string
	Topics        []string
	Removed       bool
}
type chainReceipt struct {
	TransactionHash, BlockHash, BlockNumber, Status string
	Logs                                            []chainLog
}
type chainBlock struct{ Hash, Number, Timestamp string }

func hexNum(s string) (*big.Int, error) {
	if !strings.HasPrefix(s, "0x") || len(s) < 3 {
		return nil, errors.New("invalid chain quantity")
	}
	v, ok := new(big.Int).SetString(s[2:], 16)
	if !ok || v.Sign() < 0 {
		return nil, errors.New("invalid chain quantity")
	}
	return v, nil
}
func quantity(s string) (uint64, error) {
	v, e := hexNum(s)
	if e != nil {
		return 0, e
	}
	if !v.IsUint64() {
		return 0, errors.New("chain quantity overflow")
	}
	return v.Uint64(), nil
}
func (r *ChainReader) Verify(ctx context.Context, ch Chain, o Order) error {
	if !txPattern.MatchString(o.TxHash) {
		return ErrPending
	}
	if ch.RPC != "" {
		var id string
		if e := r.call(ctx, ch, "eth_chainId", nil, &id); e != nil {
			return e
		}
		n, e := quantity(id)
		if e != nil || n != uint64(ch.ID) {
			return errors.New("RPC reports the wrong chain ID")
		}
	}
	var receipt chainReceipt
	if e := r.call(ctx, ch, "eth_getTransactionReceipt", []any{o.TxHash}, &receipt); e != nil {
		return e
	}
	if receipt.Status != "0x1" {
		return ErrPayment
	}
	var tx chainTx
	if e := r.call(ctx, ch, "eth_getTransactionByHash", []any{o.TxHash}, &tx); e != nil {
		return e
	}
	var block chainBlock
	if e := r.call(ctx, ch, "eth_getBlockByNumber", []any{receipt.BlockNumber, false}, &block); e != nil {
		return e
	}
	if e := validateTransfer(ch, o, tx, receipt, block); e != nil {
		return e
	}
	// A configured RPC must attest to finalization. Explorer-only mode uses a
	// configured confirmation depth and exposes it in the public chain config.
	if ch.RPC != "" {
		var finalized chainBlock
		if e := r.call(ctx, ch, "eth_getBlockByNumber", []any{"finalized", false}, &finalized); e != nil {
			return e
		}
		f, e := quantity(finalized.Number)
		if e != nil {
			return e
		}
		n, e := quantity(receipt.BlockNumber)
		if e != nil {
			return e
		}
		if f < n {
			return ErrPending
		}
	} else {
		var tip string
		if e := r.call(ctx, ch, "eth_blockNumber", nil, &tip); e != nil {
			return e
		}
		h, e := quantity(tip)
		if e != nil {
			return e
		}
		n, e := quantity(receipt.BlockNumber)
		if e != nil {
			return e
		}
		if ch.Confirmations == 0 || h < n || h-n < ch.Confirmations-1 {
			return ErrPending
		}
	}
	var canonical chainBlock
	if e := r.call(ctx, ch, "eth_getBlockByNumber", []any{receipt.BlockNumber, false}, &canonical); e != nil {
		return e
	}
	if !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		return ErrPending
	}
	return nil
}
func validateTransfer(ch Chain, o Order, tx chainTx, r chainReceipt, b chainBlock) error {
	if !strings.EqualFold(tx.Hash, o.TxHash) || !strings.EqualFold(r.TransactionHash, o.TxHash) || r.BlockHash == "" || !strings.EqualFold(tx.BlockHash, r.BlockHash) || !strings.EqualFold(b.Hash, r.BlockHash) || tx.BlockNumber != r.BlockNumber || b.Number != r.BlockNumber || r.Status != "0x1" || !strings.EqualFold(tx.From, o.Wallet) {
		return ErrPayment
	}
	stamp, e := quantity(b.Timestamp)
	if e != nil {
		return e
	}
	if stamp < uint64(o.Created.Unix()) || stamp > uint64(o.Expires.Unix()) {
		return ErrPayment
	}
	amount, ok := new(big.Int).SetString(o.Amount, 10)
	if !ok {
		return ErrPayment
	}
	if o.Asset == "ETH" {
		value, e := hexNum(tx.Value)
		if e != nil {
			return e
		}
		if !strings.EqualFold(tx.To, o.Recipient) || value.Cmp(amount) != 0 {
			return ErrPayment
		}
		return nil
	}
	tokenContract := ch.USDC
	if o.Asset == "NOCK_BASE" && ch.ID == 8453 {
		tokenContract = baseNockToken
	} else if o.Asset != "USDC" {
		return ErrPayment
	}
	if !strings.EqualFold(tx.To, tokenContract) {
		return ErrPayment
	}
	event := crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)")).Hex()
	from := common.BytesToHash(common.HexToAddress(o.Wallet).Bytes()).Hex()
	to := common.BytesToHash(common.HexToAddress(o.Recipient).Bytes()).Hex()
	for _, l := range r.Logs {
		if l.Removed || !strings.EqualFold(l.Address, tokenContract) || len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], event) || !strings.EqualFold(l.Topics[1], from) || !strings.EqualFold(l.Topics[2], to) {
			continue
		}
		value, e := hexNum(l.Data)
		if e == nil && value.Cmp(amount) == 0 {
			return nil
		}
	}
	return ErrPayment
}
