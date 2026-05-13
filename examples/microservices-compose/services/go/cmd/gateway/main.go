// gateway service - HTTP entrypoint that fans out to downstream RPC services.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/maxesisnclaw/nexus/pkg/sdk"
	"github.com/maxesisnclaw/nexus-demo/shared"
)

// --- downstream payload types (must match the wire schema in README.md) ---

type verifyReq struct {
	Token string `msgpack:"token"`
}
type verifyResp struct {
	Valid  bool   `msgpack:"valid"`
	UserID string `msgpack:"user_id"`
}

type catalogReq struct {
	SKU string `msgpack:"sku"`
	Qty int    `msgpack:"qty"`
}
type catalogResp struct {
	SKU       string  `msgpack:"sku"`
	Name      string  `msgpack:"name"`
	UnitPrice float64 `msgpack:"unit_price"`
	Stock     int     `msgpack:"stock"`
	Available bool    `msgpack:"available"`
}

type ordersReq struct {
	UserID    string  `msgpack:"user_id"`
	SKU       string  `msgpack:"sku"`
	Qty       int     `msgpack:"qty"`
	UnitPrice float64 `msgpack:"unit_price"`
}
type ordersResp struct {
	OrderID string  `msgpack:"order_id"`
	Total   float64 `msgpack:"total"`
}

type paymentReq struct {
	OrderID string  `msgpack:"order_id"`
	UserID  string  `msgpack:"user_id"`
	Amount  float64 `msgpack:"amount"`
}
type paymentResp struct {
	OK    bool   `msgpack:"ok"`
	TxnID string `msgpack:"txn_id"`
}

type notifierReq struct {
	UserID  string `msgpack:"user_id"`
	Channel string `msgpack:"channel"`
	Message string `msgpack:"message"`
}
type notifierResp struct {
	OK  bool `msgpack:"ok"`
	Seq int  `msgpack:"seq"`
}

// --- HTTP request / response ---

type checkoutHTTPReq struct {
	Token string `json:"token"`
	SKU   string `json:"sku"`
	Qty   int    `json:"qty"`
}

type checkoutHTTPResp struct {
	OrderID    string  `json:"order_id"`
	Total      float64 `json:"total"`
	TxnID      string  `json:"txn_id"`
	ProductName string `json:"product_name"`
	UserID     string  `json:"user_id"`
	NotifySeq  int     `json:"notify_seq"`
}

type httpError struct {
	Stage   string `json:"stage"`
	Message string `json:"error"`
}

// --- pipeline ---

func runCheckout(node *sdk.Node, in checkoutHTTPReq) (*checkoutHTTPResp, *httpError) {
	if in.SKU == "" || in.Qty <= 0 || in.Token == "" {
		return nil, &httpError{Stage: "validate", Message: "token, sku, qty(>0) required"}
	}

	// 1. auth
	var av verifyResp
	if err := call(node, "auth", "verify", verifyReq{Token: in.Token}, &av); err != nil {
		return nil, &httpError{Stage: "auth", Message: err.Error()}
	}
	if !av.Valid {
		return nil, &httpError{Stage: "auth", Message: "invalid token"}
	}

	// 2. catalog
	var cv catalogResp
	if err := call(node, "catalog", "get", catalogReq{SKU: in.SKU, Qty: in.Qty}, &cv); err != nil {
		return nil, &httpError{Stage: "catalog", Message: err.Error()}
	}
	if !cv.Available {
		return nil, &httpError{Stage: "catalog",
			Message: fmt.Sprintf("sku=%s not available (stock=%d)", cv.SKU, cv.Stock)}
	}

	// 3. orders
	var ov ordersResp
	if err := call(node, "orders", "create", ordersReq{
		UserID: av.UserID, SKU: in.SKU, Qty: in.Qty, UnitPrice: cv.UnitPrice,
	}, &ov); err != nil {
		return nil, &httpError{Stage: "orders", Message: err.Error()}
	}

	// 4. payment
	var pv paymentResp
	if err := call(node, "payment", "charge", paymentReq{
		OrderID: ov.OrderID, UserID: av.UserID, Amount: ov.Total,
	}, &pv); err != nil {
		return nil, &httpError{Stage: "payment", Message: err.Error()}
	}
	if !pv.OK {
		return nil, &httpError{Stage: "payment", Message: "charge declined"}
	}

	// 5. notifier
	var nv notifierResp
	msg := fmt.Sprintf("order %s confirmed: %s x%d = $%.2f", ov.OrderID, cv.Name, in.Qty, ov.Total)
	if err := call(node, "notifier", "send", notifierReq{
		UserID: av.UserID, Channel: "email", Message: msg,
	}, &nv); err != nil {
		return nil, &httpError{Stage: "notifier", Message: err.Error()}
	}

	return &checkoutHTTPResp{
		OrderID:     ov.OrderID,
		Total:       ov.Total,
		TxnID:       pv.TxnID,
		ProductName: cv.Name,
		UserID:      av.UserID,
		NotifySeq:   nv.Seq,
	}, nil
}

func call(node *sdk.Node, service, method string, in any, out any) error {
	resp, err := node.Call(service, method, shared.Pack(in))
	if err != nil {
		return fmt.Errorf("rpc %s.%s: %w", service, method, err)
	}
	if err := shared.Unpack(resp.Payload, out); err != nil {
		return fmt.Errorf("decode %s.%s: %w", service, method, err)
	}
	return nil
}

func main() {
	registryAddr := envOr("NEXUS_REGISTRY", "/run/nexus/registry.sock")
	listen := envOr("HTTP_LISTEN", ":8080")

	node, err := sdk.New(sdk.Config{
		Name:         "gateway",
		ID:           "gateway-1",
		RegistryAddr: registryAddr,
	})
	if err != nil {
		log.Fatalf("[gateway] new node: %v", err)
	}
	defer node.Close()
	// gateway is a pure client — still needs a UDS listener (sdk requirement?)
	// We avoid Serve since no handlers. The SDK should allow client-only usage.

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/checkout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body checkoutHTTPReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, &httpError{Stage: "decode", Message: err.Error()})
			return
		}
		log.Printf("[gateway] /checkout token=%s sku=%s qty=%d", body.Token, body.SKU, body.Qty)
		out, herr := runCheckout(node, body)
		if herr != nil {
			log.Printf("[gateway] /checkout FAIL stage=%s err=%s", herr.Stage, herr.Message)
			status := http.StatusBadGateway
			if herr.Stage == "validate" || herr.Stage == "decode" {
				status = http.StatusBadRequest
			} else if herr.Stage == "auth" {
				status = http.StatusUnauthorized
			} else if herr.Stage == "catalog" {
				status = http.StatusConflict
			}
			writeJSON(w, status, herr)
			return
		}
		log.Printf("[gateway] /checkout OK order=%s total=%.2f txn=%s",
			out.OrderID, out.Total, out.TxnID)
		writeJSON(w, http.StatusOK, out)
	})

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("[gateway] HTTP listening on %s registry=%s", listen, registryAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[gateway] http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("[gateway] shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ensure these symbols are referenced (for older Go vet)
var _ = filepath.Join
