package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"text/tabwriter"

	"github.com/maxesisn/nexus/pkg/config"
	"github.com/maxesisn/nexus/pkg/daemon"
	"github.com/maxesisn/nexus/pkg/transport"
	"github.com/vmihailenco/msgpack/v5"
)

type daemonRunner interface {
	Start(context.Context) error
	Stop() error
}

const (
	defaultConfigPath     = "./nexus.toml"
	defaultControlSocket  = "/run/nexus/registry.sock"
	defaultKeyPath        = "./nexus.key"
	maxControlMessageSize = 64 * 1024 * 1024
)

var (
	loadConfig              = config.Load
	newDaemon               = func(cfg *config.Config, logger *slog.Logger) (daemonRunner, error) { return daemon.New(cfg, logger) }
	notifyContext           = signal.NotifyContext
	stdout        io.Writer = os.Stdout
	stderr        io.Writer = os.Stderr
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "validate":
			return runValidate(args[1:])
		case "status":
			return runStatus(args[1:])
		case "keygen":
			return runKeygen(args[1:])
		}
	}
	return runDaemon(args)
}

func runDaemon(args []string) int {
	flags := flag.NewFlagSet("nexusd", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var configPath string
	flags.StringVar(&configPath, "config", defaultConfigPath, "path to nexus TOML config")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config failed: %v\n", err)
		return 1
	}

	logger := newLogger(cfg.Daemon.LogLevel)
	d, err := newDaemon(cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "create daemon failed: %v\n", err)
		return 1
	}

	ctx, stop := notifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Start(ctx); err != nil {
		logger.Error("daemon start failed", "err", err)
		return 1
	}
	logger.Info("nexus daemon started")

	<-ctx.Done()
	if err := d.Stop(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("daemon stop failed", "err", err)
		return 1
	}
	logger.Info("nexus daemon stopped")
	return 0
}

func runValidate(args []string) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var configPath string
	flags.StringVar(&configPath, "config", defaultConfigPath, "path to nexus TOML config")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(stdout, "✗ config invalid: %v\n", err)
		return 1
	}
	if _, err := daemon.ResolveStartOrder(cfg.Services); err != nil {
		fmt.Fprintf(stdout, "✗ config invalid: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "✓ config valid (%d services, dependencies OK)\n", len(cfg.Services))
	return 0
}

func runStatus(args []string) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var socketPath string
	flags.StringVar(&socketPath, "socket", defaultControlSocket, "path to daemon control socket")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(stderr, "status query failed: %v\n", err)
		return 1
	}
	defer conn.Close()

	if err := writeControlMessage(conn, statusRequest{Cmd: "status"}); err != nil {
		fmt.Fprintf(stderr, "status query failed: %v\n", err)
		return 1
	}

	var resp statusCommandResponse
	if err := readControlMessage(conn, &resp); err != nil {
		fmt.Fprintf(stderr, "status query failed: %v\n", err)
		return 1
	}
	if resp.Error != "" {
		fmt.Fprintf(stderr, "status query failed: %s\n", resp.Error)
		return 1
	}

	sort.Slice(resp.Services, func(i, j int) bool {
		if resp.Services[i].Name == resp.Services[j].Name {
			return resp.Services[i].ID < resp.Services[j].ID
		}
		return resp.Services[i].Name < resp.Services[j].Name
	})

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tID\tPID\tSTATUS")
	for _, svc := range resp.Services {
		status := "stopped"
		if svc.Running {
			status = "running"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", svc.Name, svc.ID, svc.PID, status)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "status output failed: %v\n", err)
		return 1
	}
	return 0
}

func runKeygen(args []string) int {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var outputPath string
	flags.StringVar(&outputPath, "out", defaultKeyPath, "path to write private key")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	priv, pub := transport.GenerateKeypair()
	if len(priv) != 32 || len(pub) != 32 {
		fmt.Fprintln(stderr, "generate keypair failed")
		return 1
	}

	if err := os.WriteFile(outputPath, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		fmt.Fprintf(stderr, "write private key failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "public key: %s\n", hex.EncodeToString(pub))
	fmt.Fprintf(stdout, "private key written to: %s\n", outputPath)
	return 0
}

type statusRequest struct {
	Cmd string `msgpack:"cmd"`
}

type statusService struct {
	Name    string `msgpack:"name"`
	ID      string `msgpack:"id"`
	PID     int    `msgpack:"pid"`
	Running bool   `msgpack:"running"`
}

type statusCommandResponse struct {
	OK       bool            `msgpack:"ok"`
	Error    string          `msgpack:"error"`
	Services []statusService `msgpack:"services"`
}

func writeControlMessage(w io.Writer, msg any) error {
	body, err := msgpack.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal control message: %w", err)
	}
	if len(body) > maxControlMessageSize {
		return fmt.Errorf("control message too large: %d", len(body))
	}

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func readControlMessage(r io.Reader, out any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > maxControlMessageSize {
		return fmt.Errorf("control message exceeds maximum size of %d bytes", maxControlMessageSize)
	}

	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	if err := msgpack.Unmarshal(buf, out); err != nil {
		return fmt.Errorf("decode control message: %w", err)
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h)
}
