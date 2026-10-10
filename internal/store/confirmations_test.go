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
	if err != nil || c.Chains[1].Confirmations != 20 || c.Chains[8453].Confirmations != 180 {
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

func TestProviderConfirmationThreshold(t *testing.T) {
	for _, provider := range []string{"rpc", "explorer"} {
		for _, threshold := range []uint64{20, 180} {
			t.Run(fmt.Sprintf("%s/%d", provider, threshold), func(t *testing.T) {
				t.Parallel()
				ch, o, tx, receipt, block := transferFixture()
				ch.RPC = ""
				if provider == "rpc" {
					ch.RPC = "https://rpc.example"
				}
				ch.ScanKey = "test-key"
				ch.Confirmations = threshold
				if threshold == 20 {
					ch.ID = 1
				} else {
					ch.ID = 8453
				}
				tip := uint64(16) + threshold - 2
				reorg, wrongChain := false, false
				blockCalls := 0
				reader := &ChainReader{HTTP: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
					method := req.URL.Query().Get("action")
					if provider == "rpc" {
						var call struct{ Method string }
						if err := json.NewDecoder(req.Body).Decode(&call); err != nil {
							t.Fatal(err)
						}
						method = call.Method
					}
					var result any
					switch method {
					case "eth_chainId":
						id := ch.ID
						if wrongChain {
							id++
						}
						result = fmt.Sprintf("0x%x", id)
					case "eth_getTransactionReceipt":
						result = receipt
					case "eth_getTransactionByHash":
						result = tx
					case "eth_blockNumber":
						result = fmt.Sprintf("0x%x", tip)
					case "eth_getBlockByNumber":
						blockCalls++
						copy := block
						if reorg && blockCalls == 2 {
							copy.Hash = "0x" + strings.Repeat("b", 64)
						}
						result = copy
					default:
						t.Error("unexpected request", method)
					}
					body, _ := json.Marshal(map[string]any{"result": result})
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				})}}
				if err := reader.Verify(context.Background(), ch, o); err != ErrPending {
					t.Fatal("accepted below threshold", err)
				}
				tip++
				blockCalls = 0
				if err := reader.Verify(context.Background(), ch, o); err != nil {
					t.Fatal("rejected exact threshold", err)
				}
				reorg = true
				blockCalls = 0
				if err := reader.Verify(context.Background(), ch, o); err != ErrPending {
					t.Fatal("accepted changed canonical block", err)
				}
				if provider == "rpc" {
					wrongChain = true
					if err := reader.Verify(context.Background(), ch, o); err == nil || err.Error() != "RPC reports the wrong chain ID" {
						t.Fatal("wrong chain accepted", err)
					}
				}
			})
		}
	}
}
