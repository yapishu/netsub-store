package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPaymentLedgerIncludesDestinationsWithoutSecrets(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	o.Asset = "XMR"
	o.MoneroAccount = 2
	o.MoneroIndex = 17
	o.Secret = "sensitive-planet-code"
	if err := s.save(o); err != nil {
		t.Fatal(err)
	}
	if request(s, "GET", "/api/admin/payment-ledger", "", "buyer").Code != 403 {
		t.Fatal("buyer can export payments")
	}
	w := request(s, "GET", "/api/admin/payment-ledger", "", "admin")
	var rows []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if rows[0]["recipient"] != recipient || rows[0]["subaddressIndex"] != float64(17) || rows[0]["accountIndex"] != float64(2) {
		t.Fatal(rows)
	}
	if strings.Contains(w.Body.String(), o.Secret) || strings.Contains(w.Body.String(), o.Email) {
		t.Fatal("export includes unrelated private data")
	}
}
