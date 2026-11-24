package store

import (
	"context"
	"errors"
	"fmt"
	nockchain "github.com/yapishu/nockchain-go"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

func displayPrice(amount, asset string) string {
	if asset == "NOCK" {
		n, ok := new(big.Int).SetString(amount, 10)
		if !ok || !n.IsUint64() {
			return amount
		}
		return nockchain.FormatNocks(n.Uint64())
	}
	decimals := 6
	if asset == "NOCK_BASE" {
		decimals = 16
	}
	if asset == "XMR" {
		decimals = 12
	}
	if asset == "BTC" {
		decimals = 8
	}
	if asset == "ETH" {
		decimals = 18
	}
	n, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return amount
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	text := new(big.Rat).SetFrac(n, divisor).FloatString(decimals)
	return strings.TrimRight(strings.TrimRight(text, "0"), ".")
}

func (s *Server) receipt(ctx context.Context, o Order) {
	o.ReceiptAttempts++
	o.ReceiptNext = time.Now().Add(time.Duration(1<<o.ReceiptAttempts) * time.Minute)
	if err := s.save(o); err != nil {
		s.LogError("receipt attempt save failed", err)
		return
	}
	text := s.receiptText(o)
	recipients := []string{o.Email}
	if !strings.EqualFold(o.Email, s.cfg.ReceiptTo) {
		recipients = append(recipients, s.cfg.ReceiptTo)
	}
	o.ReceiptError = ""
	for _, recipient := range recipients {
		if slices.Contains(o.ReceiptAccepted, recipient) {
			continue
		}
		if err := s.sendMail(ctx, recipient, "Receipt: "+o.Planet, text); err != nil {
			o.ReceiptError = err.Error()
			continue
		}
		o.ReceiptAccepted = append(o.ReceiptAccepted, recipient)
		if err := s.save(o); err != nil {
			s.LogError("receipt acceptance save failed", err)
			return
		}
	}
	o.ReceiptSent = o.ReceiptError == ""
	if err := s.save(o); err != nil {
		s.LogError("receipt status save failed", err)
	}
}

func (s *Server) retryReceipt(w http.ResponseWriter, r *http.Request) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		o, err := getOrder(tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if o.PaidAt.IsZero() {
			return errors.New("Payment has not been verified")
		}
		if o.ReceiptSent {
			o.ReceiptAccepted = nil
		}
		o.ReceiptSent = false
		o.ReceiptAttempts = 0
		o.ReceiptNext = time.Now()
		o.ReceiptError = ""
		return putOrder(tx, o)
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true})
}

func (s *Server) receiptText(o Order) string {
	chain := s.cfg.Chains[o.ChainID]
	if o.Asset == "BTC" {
		chain.Name = "Bitcoin (" + o.BitcoinNetwork + ")"
	}
	if o.Asset == "XMR" {
		chain.Name = "Monero (" + o.MoneroNetwork + ")"
	}
	if o.Asset == "NOCK" {
		chain.Name = "Nockchain"
	}
	transactions := o.TxHash
	if len(o.BitcoinTxIDs) > 0 {
		transactions = strings.Join(o.BitcoinTxIDs, ", ")
	}
	if len(o.MoneroTxIDs) > 0 {
		transactions = strings.Join(o.MoneroTxIDs, ", ")
	}
	if len(o.NockTxIDs) > 0 {
		transactions = strings.Join(o.NockTxIDs, ", ")
	}
	purchase := o.Tier
	if o.NameAndSigil {
		purchase = "premium name + sigil"
	}
	if o.Random {
		purchase = "random standard"
	}
	return fmt.Sprintf("Payment received\n\nOrder: %s\nPlanet: %s\nSelection: %s\nAmount: %s %s\nNetwork: %s\nTransaction: %s\nBuyer wallet: %s\nDelivery email: %s\nPaid at: %s\n\nThe planet code is delivered separately to the buyer. View the order at %s.\n",
		o.ID, o.Planet, purchase, displayPrice(o.Amount, o.Asset), paymentAssetName(o.Asset), chain.Name, transactions, o.Wallet, o.Email, o.PaidAt.UTC().Format(time.RFC3339), s.cfg.Origin)
}
