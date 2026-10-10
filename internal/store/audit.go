package store

import (
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

type Event struct {
	NockConfirmations   int      `json:"nockConfirmations,omitempty"`
	NockReceived        string   `json:"nockReceived,omitempty"`
	NockTxIDs           []string `json:"nockTxIds,omitempty"`
	NameAndSigil        bool     `json:"nameAndSigil,omitempty"`
	MoneroNetwork       string   `json:"moneroNetwork,omitempty"`
	MoneroConfirmations int      `json:"moneroConfirmations,omitempty"`
	MoneroReceived      string   `json:"moneroReceived,omitempty"`
	MoneroTxIDs         []string `json:"moneroTxIds,omitempty"`

	BitcoinNetwork       string    `json:"bitcoinNetwork,omitempty"`
	BitcoinConfirmations int       `json:"bitcoinConfirmations,omitempty"`
	BitcoinReceived      string    `json:"bitcoinReceived,omitempty"`
	BitcoinTxIDs         []string  `json:"bitcoinTxIds,omitempty"`
	ID                   uint64    `json:"id"`
	At                   time.Time `json:"at"`
	Kind                 string    `json:"kind"`
	RequestID            string    `json:"requestId,omitempty"`
	Action               string    `json:"action,omitempty"`
	HTTPStatus           int       `json:"httpStatus,omitempty"`
	OrderID              string    `json:"orderId,omitempty"`
	Planet               string    `json:"planet,omitempty"`
	Wallet               string    `json:"wallet,omitempty"`
	Status               string    `json:"status,omitempty"`
	Tier                 string    `json:"tier,omitempty"`
	Random               bool      `json:"random,omitempty"`
	Asset                string    `json:"asset,omitempty"`
	Amount               string    `json:"amount,omitempty"`
	ChainID              int       `json:"chainId,omitempty"`
	TxHash               string    `json:"txHash,omitempty"`
	PaymentChecks        int       `json:"checks,omitempty"`
	EmailAttempts        int       `json:"emailAttempts,omitempty"`
	ReceiptAttempts      int       `json:"receiptAttempts,omitempty"`
	EmailSent            bool      `json:"emailSent,omitempty"`
	ReceiptSent          bool      `json:"receiptSent,omitempty"`
	Error                string    `json:"error,omitempty"`
	PaymentConfirmedBy   string    `json:"paymentConfirmedBy,omitempty"`
	ReceiptError         string    `json:"receiptError,omitempty"`
}

func appendEvent(tx *bolt.Tx, event Event) error {
	bucket := tx.Bucket([]byte("events"))
	id, err := bucket.NextSequence()
	if err != nil {
		return err
	}
	event.ID, event.At = id, time.Now().UTC()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], id)
	if err := bucket.Put(key[:], data); err != nil {
		return err
	}
	tx.OnCommit(func() { slog.Info("store event", "event", event) })
	return nil
}

func (s *Server) event(event Event) error {
	return s.db.Update(func(tx *bolt.Tx) error { return appendEvent(tx, event) })
}

type auditResponse struct {
	http.ResponseWriter
	status int
}

func (w *auditResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *auditResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (s *Server) auditRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == "GET" || r.Method == "HEAD") || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		// Match endpoint families, never record request bodies, query strings or credentials.
		action := r.Method + " api"
		switch {
		case r.URL.Path == "/api/orders":
			action = r.Method + " checkout"
		case strings.HasPrefix(r.URL.Path, "/api/orders/"):
			action = r.Method + " payment"
		case strings.HasPrefix(r.URL.Path, "/api/admin/"):
			action = r.Method + " admin"
		case strings.HasPrefix(r.URL.Path, "/api/auth/"):
			action = r.Method + " authentication"
		}
		actor, _ := s.current(r)
		event := Event{Kind: "request.started", RequestID: token(), Action: action, Wallet: actor.Wallet}
		if err := s.event(event); err != nil {
			s.LogError("attempt persistence failed", err)
			fail(w, 503, "Cannot record attempt; try again later")
			return
		}
		response := &auditResponse{ResponseWriter: w}
		next.ServeHTTP(response, r)
		event.Kind = "request.finished"
		event.HTTPStatus = response.status
		if id := r.PathValue("id"); id != "" {
			_ = s.db.View(func(tx *bolt.Tx) error {
				if order, err := getOrder(tx, id); err == nil {
					event.OrderID = order.ID
				}
				return nil
			})
		}
		if err := s.event(event); err != nil {
			s.LogError("attempt result persistence failed", err)
		}
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseUint(r.URL.Query().Get("before"), 10, 64)
	events := []Event{}
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte("events")).Cursor()
		var key, value []byte
		if before == 0 {
			key, value = cursor.Last()
		} else {
			var encoded [8]byte
			binary.BigEndian.PutUint64(encoded[:], before)
			key, value = cursor.Seek(encoded[:])
			if key == nil {
				key, value = cursor.Last()
			} else {
				key, value = cursor.Prev()
			}
		}
		for ; key != nil && len(events) < 100; key, value = cursor.Prev() {
			var event Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	if err != nil {
		fail(w, 500, "Cannot read activity")
		return
	}
	respond(w, events)
}
