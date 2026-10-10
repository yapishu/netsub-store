package store

import (
	bolt "go.etcd.io/bbolt"
	"net/http"
)

func (s *Server) paymentLedger(w http.ResponseWriter, r *http.Request) {
	rows := []map[string]any{}
	err := s.db.View(func(tx *bolt.Tx) error {
		orders, err := allOrders(tx)
		if err != nil {
			return err
		}
		for _, o := range orders {
			row := map[string]any{"orderId": o.ID, "asset": o.Asset, "amount": o.Amount, "recipient": o.Recipient, "status": o.Status, "created": o.Created, "paidAt": o.PaidAt, "paymentConfirmedBy": o.PaymentConfirmedBy, "chainId": o.ChainID, "transaction": o.TxHash}
			switch o.Asset {
			case "BTC":
				row["network"] = o.BitcoinNetwork
				row["transactions"] = o.BitcoinTxIDs
				row["walletLabel"] = "netsub-order:" + o.ID
			case "XMR":
				row["network"] = o.MoneroNetwork
				row["transactions"] = o.MoneroTxIDs
				row["accountIndex"] = o.MoneroAccount
				row["subaddressIndex"] = o.MoneroIndex
			case "NOCK":
				row["transactions"] = o.NockTxIDs
				row["childIndex"] = o.NockIndex
				row["lockRoot"] = o.NockLockRoot
				row["receivingBranchId"] = o.NockKeyID
				if root := tx.Bucket([]byte("nockchain-accounts")); root != nil {
					if account := root.Bucket([]byte(o.NockKeyID)); account != nil {
						row["receivingZpub"] = string(account.Get([]byte("zpub")))
					}
				}
			}
			rows = append(rows, row)
		}
		return nil
	})
	if err != nil {
		fail(w, 500, "Cannot export payment destinations")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="payment-destinations.json"`)
	respond(w, rows)
}
