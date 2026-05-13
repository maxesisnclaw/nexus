// payment service - mock charge.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/maxesisnclaw/nexus/pkg/sdk"
	"github.com/maxesisnclaw/nexus-demo/shared"
)

type chargeReq struct {
	OrderID string  `msgpack:"order_id"`
	UserID  string  `msgpack:"user_id"`
	Amount  float64 `msgpack:"amount"`
}

type chargeResp struct {
	OK    bool   `msgpack:"ok"`
	TxnID string `msgpack:"txn_id"`
}

func newTxnID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "txn-" + hex.EncodeToString(b)
}

func main() {
	registryAddr := envOr("NEXUS_REGISTRY", "/run/nexus/registry.sock")
	uds := envOr("NEXUS_UDS", "/run/nexus/svc/payment.sock")
	_ = os.MkdirAll(filepath.Dir(uds), 0o755)
	_ = os.Remove(uds)

	node, err := sdk.New(sdk.Config{
		Name:         "payment",
		ID:           "payment-1",
		UDSAddr:      uds,
		RegistryAddr: registryAddr,
	})
	if err != nil {
		log.Fatalf("[payment] new node: %v", err)
	}
	defer node.Close()

	node.HandleFunc("charge", func(req *sdk.Request) (*sdk.Response, error) {
		var in chargeReq
		if err := shared.Unpack(req.Payload, &in); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		if in.Amount <= 0 {
			return nil, fmt.Errorf("invalid amount")
		}
		out := chargeResp{OK: true, TxnID: newTxnID()}
		log.Printf("[payment] charge order=%s user=%s amount=%.2f -> %s",
			in.OrderID, in.UserID, in.Amount, out.TxnID)
		return &sdk.Response{Payload: shared.Pack(out)}, nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("[payment] starting on %s registry=%s", uds, registryAddr)
	if err := node.Serve(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("[payment] serve: %v", err)
	}
	log.Printf("[payment] exited")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
