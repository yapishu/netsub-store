package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"netsub-store/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "recover":
			if len(os.Args) != 4 {
				log.Fatal("usage: store recover ORDER_ID OUTPUT.json")
			}
			c, e := store.FromEnv()
			if e != nil {
				log.Fatal(e)
			}
			if e = store.Recover(c, os.Args[2], os.Args[3]); e != nil {
				log.Fatal(e)
			}
			fmt.Println("Recovery bundle written with owner-only permissions.")
			return
		case "encryption-key":
			b := make([]byte, 32)
			if _, e := rand.Read(b); e != nil {
				log.Fatal(e)
			}
			fmt.Println(hex.EncodeToString(b))
			return
		default:
			log.Fatal("usage: store [encryption-key|recover ORDER_ID OUTPUT.json]")
		}
	}
	cfg, e := store.FromEnv()
	if e != nil {
		log.Fatal(e)
	}
	s, e := store.New(cfg)
	if e != nil {
		log.Fatal(e)
	}
	defer s.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); s.Run(ctx) }()
	server := &http.Server{Addr: cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Networked Subject store listening on %s (preview=%v)", cfg.Listen, cfg.Demo)
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		cancel()
		log.Print(e)
	}
	cancel()
	<-workerDone
}
