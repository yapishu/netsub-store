package store

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (s *Server) confirmPayment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !in.Confirm {
		fail(w, 400, "Confirm that you have checked the payment before starting delivery")
		return
	}
	actor, _ := s.current(r)
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		o, err := getOrder(tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		switch o.Status {
		case "awaiting_address", "awaiting_payment", "confirming", "review_payment":
		default:
			return errors.New("Only orders awaiting payment can be confirmed manually")
		}
		if !o.PaidAt.IsZero() || o.Secret != "" {
			return errors.New("This order already has payment or delivery recorded")
		}
		if !addressPaymentAsset(o.Asset) && o.TxHash != "" {
			key := []byte(fmt.Sprintf("%d:%s", o.ChainID, o.TxHash))
			payments := tx.Bucket([]byte("payments"))
			if used := payments.Get(key); used != nil && string(used) != o.ID {
				return errors.New("Payment is already used by another order")
			}
			if err := payments.Put(key, []byte(o.ID)); err != nil {
				return err
			}
		}
		o.Status = "paid"
		o.PaidAt = time.Now().UTC()
		o.PaymentConfirmedBy = actor.Wallet
		o.ReceiptNext, o.NextCheck = o.PaidAt, o.PaidAt
		o.Checks, o.Error = 0, ""
		if err := putOrder(tx, o); err != nil {
			return err
		}
		return appendEvent(tx, Event{Kind: "payment.manually_confirmed", OrderID: o.ID, Planet: o.Planet, Wallet: o.Wallet, PaymentConfirmedBy: actor.Wallet, Asset: o.Asset, Amount: o.Amount, ChainID: o.ChainID, TxHash: o.TxHash, Status: o.Status})
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true})
}
