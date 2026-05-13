// orders service - generates order IDs and computes totals.
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

type createReq struct {
	UserID    string  `msgpack:"user_id"`
	SKU       string  `msgpack:"sku"`
	Qty       int     `msgpack:"qty"`
	UnitPrice float64 `msgpack:"unit_price"`
}

type createResp struct {
	OrderID string  `msgpack:"order_id"`
	Total   float64 `msgpack:"total"`
}

func newOrderID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "ord-" + hex.EncodeToString(b)
}

func main() {
	registryAddr := envOr("NEXUS_REGISTRY", "/run/nexus/registry.sock")
	uds := envOr("NEXUS_UDS", "/run/nexus/svc/orders.sock")
	_ = os.MkdirAll(filepath.Dir(uds), 0o755)
	_ = os.Remove(uds)

	node, err := sdk.New(sdk.Config{
		Name:         "orders",
		ID:           "orders-1",
		UDSAddr:      uds,
		RegistryAddr: registryAddr,
	})
	if err != nil {
		log.Fatalf("[orders] new node: %v", err)
	}
	defer node.Close()

	node.HandleFunc("create", func(req *sdk.Request) (*sdk.Response, error) {
		var in createReq
		if err := shared.Unpack(req.Payload, &in); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		if in.Qty <= 0 || in.UnitPrice <= 0 {
			return nil, fmt.Errorf("invalid qty/unit_price")
		}
		out := createResp{
			OrderID: newOrderID(),
			Total:   float64(in.Qty) * in.UnitPrice,
		}
		log.Printf("[orders] create user=%s sku=%s qty=%d -> id=%s total=%.2f",
			in.UserID, in.SKU, in.Qty, out.OrderID, out.Total)
		return &sdk.Response{Payload: shared.Pack(out)}, nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("[orders] starting on %s registry=%s", uds, registryAddr)
	if err := node.Serve(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("[orders] serve: %v", err)
	}
	log.Printf("[orders] exited")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
