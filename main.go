package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"transporter/client"
	"transporter/server"
	"transporter/transport"
)

type args struct {
	mode        string
	socksListen string
	transport   string
	verbose     bool
}

type cliFlags struct {
	mode      *string
	socks     *string
	verbose   *bool
	transport *string
	config    *string
}

func main() {
	flagPtrs := registerFlags()
	transport.AddFlags(flag.CommandLine)
	loadConfigDefaults()
	flag.Parse()

	args := extractArgs(flagPtrs)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var role transport.Role
	switch strings.ToLower(args.mode) {
	case "client":
		role = transport.RoleClient
	case "server":
		role = transport.RoleServer
	default:
		log.Fatalf("unknown mode %q", args.mode)
	}

	desc := transport.Describe(args.transport, role)
	conn, err := transport.Open(ctx, role, args.transport)
	if err != nil {
		log.Fatalf("open transport: %v", err)
	}
	defer conn.Close()

	switch role {
	case transport.RoleClient:
		logMessage(
			args.verbose,
			"client mode: starting SOCKS5 on %s via %s (%s)",
			args.socksListen,
			args.transport,
			desc,
		)
		err = client.Run(ctx, client.Config{
			SocksListen: args.socksListen,
			Transport:   conn,
			Verbose:     args.verbose,
		})
	case transport.RoleServer:
		logMessage(args.verbose, "server mode: awaiting frames via %s (%s)", args.transport, desc)
		err = server.Run(ctx, server.Config{
			Transport: conn,
			Verbose:   args.verbose,
		})
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("%s mode exited: %v", args.mode, err)
	}
}

func registerFlags() cliFlags {
	mode := flag.String("mode", "", "client or server")
	socksListen := flag.String("socks", "127.0.0.1:1080", "SOCKS5 listen address (client mode)")
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	config := flag.String("config", "", "path to JSON config file")
	defaultTransport := transport.DefaultName()
	names := transport.Names()
	if defaultTransport == "" {
		log.Fatal("no transports registered")
	}
	transportArg := flag.String(
		"transport",
		defaultTransport,
		fmt.Sprintf("transport implementation (%v)", names),
	)
	return cliFlags{
		mode:      mode,
		socks:     socksListen,
		verbose:   verbose,
		transport: transportArg,
		config:    config,
	}
}

func extractArgs(p cliFlags) args {
	if *p.mode == "" {
		fmt.Fprintln(os.Stderr, "Error: --mode must be either client or server")
		flag.Usage()
		os.Exit(2)
	}
	return args{
		mode:        *p.mode,
		socksListen: *p.socks,
		transport:   *p.transport,
		verbose:     *p.verbose,
	}
}

func logMessage(verbose bool, format string, args ...interface{}) {
	if verbose {
		log.Printf(format, args...)
	}
}

func loadConfigDefaults() {
	path := detectConfigPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read config %s: %v", path, err)
	}
	_ = flag.CommandLine.Set("config", path)

	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parse config %s: %v", path, err)
	}

	setFlagIfValue("mode", cfg.Mode)
	setFlagIfValue("socks", cfg.Socks)
	setFlagIfValue("transport", cfg.Transport)
	if cfg.Verbose != nil {
		setFlagIfValue("verbose", fmt.Sprintf("%t", *cfg.Verbose))
	}
	for key, val := range cfg.Flags {
		setFlagIfValue(key, val)
	}
}

func detectConfigPath() string {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-config="):
			return strings.TrimPrefix(arg, "-config=")
		case arg == "-config" || arg == "--config":
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	if env := os.Getenv("TRANSPORTER_CONFIG"); env != "" {
		return env
	}
	return ""
}

func setFlagIfValue(name, value string) {
	if value == "" {
		return
	}
	if err := flag.CommandLine.Set(name, value); err != nil {
		log.Fatalf("invalid config option %s=%s: %v", name, value, err)
	}
}

type fileConfig struct {
	Mode      string            `json:"mode"`
	Socks     string            `json:"socks"`
	Transport string            `json:"transport"`
	Verbose   *bool             `json:"verbose"`
	Flags     map[string]string `json:"flags"`
}
