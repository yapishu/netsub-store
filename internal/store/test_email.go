package store

import (
	"net/http"
	"net/mail"
	"time"
)

type emailSample struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

func (s *Server) emailSamples() map[string]emailSample {
	o := Order{ID: "TEST-ORDER", Planet: "~sampel-palnet", Tier: "standard", Asset: "USDC", ChainID: 8453, Amount: "10000000", TxHash: "TEST — no payment", Wallet: s.cfg.AdminWallet, Email: "buyer@example.com", PaidAt: time.Now().UTC()}
	return map[string]emailSample{
		"delivery": {Subject: "[TEST] Your planet code for ~sampel-palnet", Text: "TEST EMAIL — Sample data; this code cannot be redeemed.\n\n" + deliveryText(o.Planet, "SAMPLE-CODE-NOT-REDEEMABLE", "https://bridge.urbit.org", s.cfg.Origin)},
		"receipt":  {Subject: "[TEST] Receipt: ~sampel-palnet", Text: "TEST EMAIL — Sample data; no payment or purchase.\n\n" + s.receiptText(o)},
	}
}

func (s *Server) emailPreview(w http.ResponseWriter, r *http.Request) { respond(w, s.emailSamples()) }

func (s *Server) testEmail(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Kind string }
	if !readJSON(w, r, &in) {
		return
	}
	sample, ok := s.emailSamples()[in.Kind]
	recipient, err := mail.ParseAddress(in.Email)
	if !ok || err != nil || recipient.Address != in.Email || len(in.Email) > 254 {
		fail(w, 400, "Choose a template and enter a valid email address")
		return
	}
	if s.cfg.Demo || (s.cfg.CloudflareEmailToken == "" || s.cfg.CloudflareAccountID == "") {
		fail(w, 409, "Email sending requires a live store with Cloudflare configured")
		return
	}
	if !s.auth.allow("test-email", 3, time.Minute) {
		fail(w, 429, "Wait a minute before sending another test email")
		return
	}
	if err := s.sendMail(r.Context(), in.Email, sample.Subject, sample.Text); err != nil {
		fail(w, 502, err.Error())
		return
	}
	respond(w, map[string]string{"message": "Cloudflare accepted the test email to " + in.Email + ". Check your inbox and spam folder."})
}
