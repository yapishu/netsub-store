package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConfirmationEnvironment(t *testing.T) {
	for k, v := range map[string]string{"DEMO": "true", "PUBLIC_ORIGIN": "https://store.test", "DATA_ENCRYPTION_KEY": strings.Repeat("1", 64), "ADMIN_WALLET_ADDRESS": buyer, "ETH_CONFIRMATIONS": "", "BASE_CONFIRMATIONS": ""} {
		t.Setenv(k, v)
	}
	c, err := FromEnv()
	if err != nil || c.Chains[1].Confirmations != 3 || c.Chains[8453].Confirmations != 12 {
		t.Fatal("incorrect defaults", err)
	}
	t.Setenv("ETH_CONFIRMATIONS", "6")
	t.Setenv("BASE_CONFIRMATIONS", "30")
	c, err = FromEnv()
	if err != nil || c.Chains[1].Confirmations != 6 || c.Chains[8453].Confirmations != 30 {
		t.Fatal("custom thresholds ignored", err)
	}
	for _, name := range []string{"ETH_CONFIRMATIONS", "BASE_CONFIRMATIONS"} {
		for _, value := range []string{"0", "-1", "1.5", "no", "100001", "18446744073709551616"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				t.Setenv(name, value)
				if _, err := FromEnv(); err == nil {
					t.Fatal("invalid threshold accepted")
				}
			})
		}
	}
}

func TestExplorerConfirmationThreshold(t *testing.T) {
	for _, threshold := range []uint64{3, 12} {
		t.Run(fmt.Sprint(threshold), func(t *testing.T) {
			t.Parallel()
			ch, o, tx, receipt, block := transferFixture()
			ch.RPC = ""
			ch.ScanKey = "test-key"
			ch.Confirmations = threshold
			if threshold == 3 {
				ch.ID = 1
			} else {
				ch.ID = 8453
			}
			tip := uint64(16) + threshold - 2
			reorg := false
			reader := &ChainReader{HTTP: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				var result any
				switch req.URL.Query().Get("action") {
				case "eth_getTransactionReceipt":
					result = receipt
				case "eth_getTransactionByHash":
					result = tx
				case "eth_blockNumber":
					result = fmt.Sprintf("0x%x", tip)
				case "eth_getBlockByNumber":
					copy := block
					if reorg {
						copy.Hash = "0x" + strings.Repeat("b", 64)
					}
					result = copy
				default:
					t.Error("unexpected request", req.URL.Query().Get("action"))
				}
				body, _ := json.Marshal(map[string]any{"result": result})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}}
			if err := reader.Verify(context.Background(), ch, o); err != ErrPending {
				t.Fatal("accepted below threshold", err)
			}
			tip++
			if err := reader.Verify(context.Background(), ch, o); err != nil {
				t.Fatal("rejected exact threshold", err)
			}
			reorg = true
			if err := reader.Verify(context.Background(), ch, o); err == nil {
				t.Fatal("accepted changed block")
			}
		})
	}
}
