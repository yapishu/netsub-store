package store

import (
	"encoding/json"
	bolt "go.etcd.io/bbolt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManualPaymentConfirmation(t *testing.T) {
	for _, asset := range []string{"ETH", "USDC", "NOCK_BASE", "BTC", "XMR", "NOCK"} {
		t.Run(asset, func(t *testing.T) {
			s := testServer(t)
			admin := s.auth.sessions["admin"]
			admin.Wallet = buyer
			s.auth.sessions["admin"] = admin
			o := create(t, s, "~sampel-palnet")
			o.Asset, o.Status, o.TxHash = asset, "confirming", hash
			o.NextCheck, o.Checks, o.Error = time.Now().Add(time.Hour), 9, "Waiting"
			if err := s.save(o); err != nil {
				t.Fatal(err)
			}
			path := "/api/admin/orders/" + o.ID + "/confirm-payment"
			for _, cookie := range []string{"", "buyer"} {
				if w := request(s, "POST", path, `{"confirm":true}`, cookie); w.Code != map[string]int{"": 401, "buyer": 403}[cookie] {
					t.Fatalf("non-admin accepted: %d", w.Code)
				}
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(`{"confirm":true}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", s.cfg.Origin)
			r.AddCookie(&http.Cookie{Name: "netsub_session", Value: "admin"})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("missing CSRF accepted: %d", w.Code)
			}
			if w := request(s, "POST", path, `{}`, "admin"); w.Code != 400 {
				t.Fatal("confirmation not required", w.Code)
			}
			if got := readOrder(t, s, o.ID); got.Status != "confirming" {
				t.Fatal("rejected request changed order")
			}
			if w := request(s, "POST", path, `{"confirm":true}`, "admin"); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			got := readOrder(t, s, o.ID)
			if got.Status != "paid" || got.PaymentConfirmedBy != buyer || got.PaidAt.IsZero() || got.Checks != 0 || got.Error != "" || got.NextCheck.After(time.Now()) || got.ReceiptNext != got.PaidAt || got.Secret != "" {
				t.Fatalf("bad transition: status=%s", got.Status)
			}
			found := false
			s.db.View(func(tx *bolt.Tx) error {
				return tx.Bucket([]byte("events")).ForEach(func(_, v []byte) error {
					var e Event
					json.Unmarshal(v, &e)
					if e.Kind == "payment.manually_confirmed" && e.OrderID == o.ID && e.PaymentConfirmedBy == buyer {
						found = true
					}
					return nil
				})
			})
			if !found {
				t.Fatal("manual confirmation not audited")
			}
			if w := request(s, "POST", path, `{"confirm":true}`, "admin"); w.Code != 409 {
				t.Fatal("confirmed twice", w.Code)
			}
		})
	}
}

func TestManualPaymentRejectsClosedOrdersAndReplay(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	path := "/api/admin/orders/" + o.ID + "/confirm-payment"
	for _, status := range []string{"expired", "paid", "prepared", "spawn_submitted", "review_fulfillment", "delivered"} {
		o.Status = status
		s.save(o)
		if w := request(s, "POST", path, `{"confirm":true}`, "admin"); w.Code != 409 {
			t.Fatal("accepted status", status, w.Code)
		}
	}
	o.Status, o.Asset, o.TxHash = "review_payment", "ETH", hash
	s.save(o)
	s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("payments")).Put([]byte("8453:"+hash), []byte("other-order"))
	})
	if w := request(s, "POST", path, `{"confirm":true}`, "admin"); w.Code != 409 {
		t.Fatal("payment replay accepted", w.Code)
	}
	if readOrder(t, s, o.ID).Status != "review_payment" {
		t.Fatal("replay changed order")
	}
}
