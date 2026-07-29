// Command doublangu-server starts the secure single-owner core server.
// Plugin loading is assembled separately by the application runtime.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"doublangu/internal/auth"
	"doublangu/internal/config"
	"doublangu/internal/httpapi"
	"doublangu/internal/httpapi/pluginassets"
	manifest "doublangu/internal/plugins"
	"doublangu/internal/store"
	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the production entrypoint, separated for deterministic CLI tests.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--create-owner=") || strings.HasPrefix(arg, "--reset-owner=") {
			fmt.Fprintln(stderr, "owner action flags do not accept password values")
			return 2
		}
	}
	flags := flag.NewFlagSet("doublangu-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	createOwner := flags.Bool("create-owner", false, "create the single owner; read the password securely from stdin")
	resetOwner := flags.Bool("reset-owner", false, "reset the single owner and revoke sessions; read password securely from stdin")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected command argument")
		return 2
	}
	if *createOwner && *resetOwner {
		fmt.Fprintln(stderr, "--create-owner and --reset-owner are mutually exclusive")
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "configuration: %v\n", err)
		return 1
	}
	if err := ensureDatabaseParent(cfg.Database.Path); err != nil {
		fmt.Fprintf(stderr, "database directory: %v\n", err)
		return 1
	}

	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		fmt.Fprintf(stderr, "database: %v\n", err)
		return 1
	}
	defer db.Close()

	ownerManager := auth.NewOwnerManager(db)
	if *createOwner || *resetOwner {
		password, err := readOwnerPassword(stdin, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "owner password: %v\n", err)
			return 1
		}
		if err := ownerManager.CreateOwner(context.Background(), password, *resetOwner); err != nil {
			fmt.Fprintf(stderr, "owner: %v\n", err)
			return 1
		}
		if *resetOwner {
			fmt.Fprintln(stdout, "Owner reset successfully.")
		} else {
			fmt.Fprintln(stdout, "Owner created successfully.")
		}
		return 0
	}

	authHandler := &auth.AuthHandler{
		Sessions:      auth.NewSessionStore(db),
		OwnerManager:  ownerManager,
		CSRF:          auth.NewCSRF(cfg.Secret),
		RateLimiter:   auth.LoginRateLimiter(),
		SessionMaxAge: cfg.Session.MaxAge,
		Secure:        cfg.Session.Secure,
	}
	healthHandler := httpapi.NewHealthHandler(db)
	schema, err := manifest.LoadSchema()
	if err != nil {
		fmt.Fprintf(stderr, "WARNING: schema not available: %v\n", err)
	}
	registry := manifest.NewRegistry()
	if err := serve(cfg.Listen, registry, schema, db, newHandler(registry, schema, authHandler, healthHandler), stdout); err != nil {
		fmt.Fprintf(stderr, "server: %v\n", err)
		return 1
	}
	return 0
}

func ensureDatabaseParent(databasePath string) error {
	parent := filepath.Dir(databasePath)
	if parent == "." {
		return nil
	}
	return os.MkdirAll(parent, 0o755)
}

func newHandler(
	registry *manifest.Registry,
	schema *manifest.ParsedSchema,
	authHandler *auth.AuthHandler,
	health *httpapi.HealthHandler,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", httpapi.ErrCodeMethodNotAllow)
			return
		}
		httpapi.WriteOK(w, manifest.CollectDiagnostics(registry, schema))
	})
	mux.HandleFunc("/live", health.ServeLive)
	mux.HandleFunc("/ready", health.ServeReady)

	uiContributions := authHandler.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", httpapi.ErrCodeMethodNotAllow)
			return
		}
		snapshot, err := registry.UIContributions()
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "ui contributions unavailable", httpapi.ErrCodeInternal)
			return
		}
		httpapi.WriteOK(w, snapshot)
	}))
	mux.Handle("/api/v1/ui/contributions", uiContributions)

	mux.HandleFunc("/api/v1/auth/csrf", authHandler.ServeCSRF)
	mux.HandleFunc("/api/v1/auth/login", authHandler.ServeLogin)
	mux.HandleFunc("/api/v1/auth/logout", authHandler.ServeLogout)
	mux.HandleFunc("/api/v1/auth/session", authHandler.ServeSessionCheck)

	assets, err := pluginassets.New(pluginAssetsRoot(), pluginassets.DefaultPrefix, authHandler.AuthorizeFunc())
	if err != nil {
		// This keeps CP7 asset failures as their established non-JSON contract.
		mux.HandleFunc(pluginassets.DefaultPrefix, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "plugin assets unavailable", http.StatusInternalServerError)
		})
	} else {
		mux.Handle(pluginassets.DefaultPrefix, assets)
	}

	return mux
}

func pluginAssetsRoot() string {
	if root := os.Getenv("DOUBLANGU_PLUGIN_ASSETS"); root != "" {
		return root
	}
	return "web/static/plugin-assets"
}

func serve(address string, registry *manifest.Registry, schema *manifest.ParsedSchema, db *store.DB, handler http.Handler, output io.Writer) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &http.Server{Handler: handler}
	fmt.Fprint(output, startupBanner(registry, schema, db))
	fmt.Fprintf(output, "listening on %s\n", listener.Addr())

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stop.Done():
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func startupBanner(registry *manifest.Registry, schema *manifest.ParsedSchema, db *store.DB) string {
	var b strings.Builder
	b.WriteString(manifest.ZeroPluginBanner(registry, schema))
	dbStatus := "ok"
	if err := db.Conn().Ping(); err != nil {
		dbStatus = "error"
	}
	fmt.Fprintf(&b, "database: %s\n", dbStatus)
	return b.String()
}

// readOwnerPassword keeps passwords out of argv and normal output. Interactive
// terminals use hidden input; pipes and buffers deliberately support automation.
func readOwnerPassword(input io.Reader, output io.Writer) (string, error) {
	fmt.Fprint(output, "Owner password: ")
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		password, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(output)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		if len(password) == 0 {
			return "", errors.New("password must not be empty")
		}
		return string(password), nil
	}

	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	return password, nil
}
