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

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/auth"
	"doublangu/internal/config"
	"doublangu/internal/httpapi"
	"doublangu/internal/httpapi/pluginassets"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/media"
	manifest "doublangu/internal/plugins"
	"doublangu/internal/reader"
	"doublangu/internal/store"
	"doublangu/internal/workers"
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
	if _, err := jobs.NewStore(db).RecoverExpired(context.Background()); err != nil {
		fmt.Fprintf(stderr, "job recovery: %v\n", err)
		return 1
	}
	mediaStore, err := media.New(cfg.Paths.Media)
	if err != nil {
		fmt.Fprintf(stderr, "media: %v\n", err)
		return 1
	}
	if err := mediaStore.Recover(context.Background(), db); err != nil {
		fmt.Fprintf(stderr, "media recovery: %v\n", err)
		return 1
	}
	articleStore := reader.NewStoreWithMedia(db, mediaStore)
	if err := articleStore.RecoverInterrupted(context.Background()); err != nil {
		fmt.Fprintf(stderr, "article enrichment recovery: %v\n", err)
		return 1
	}
	var articleAnnotator annotator.Annotator
	if cfg.Annotator == "disabled" {
		articleAnnotator = annotator.Disabled{}
	} else {
		articleAnnotator = annotator.NewCodexAppServer(annotator.CodexConfig{
			Model:  cfg.CodexModel,
			Effort: cfg.CodexEffort,
		})
	}
	healthHandler := httpapi.NewHealthHandler(db)
	schema, err := manifest.LoadSchema()
	if err != nil {
		fmt.Fprintf(stderr, "WARNING: schema not available: %v\n", err)
	}
	registry := manifest.NewRegistry()
	semanticProvider, _ := articleAnnotator.(annotator.SemanticAnnotator)
	analysisRunner := analysis.NewRunnerWithMedia(db, semanticProvider, mediaStore)
	analysisContext, stopAnalysis := context.WithCancel(context.Background())
	go analysisRunner.Run(analysisContext)
	defer stopAnalysis()
	if err := serve(cfg.Listen, registry, schema, db, newHandlerWithMedia(registry, schema, authHandler, healthHandler, cfg, db, mediaStore, articleAnnotator), stdout); err != nil {
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
	cfg *config.Config,
	db *store.DB,
	providers ...annotator.Annotator,
) http.Handler {
	return newHandlerWithMedia(registry, schema, authHandler, health, cfg, db, nil, providers...)
}

func newHandlerWithMedia(
	registry *manifest.Registry,
	schema *manifest.ParsedSchema,
	authHandler *auth.AuthHandler,
	health *httpapi.HealthHandler,
	cfg *config.Config,
	db *store.DB,
	mediaStore *media.Store,
	providers ...annotator.Annotator,
) http.Handler {
	mux := http.NewServeMux()
	if mediaStore == nil {
		mediaStore, _ = media.New(cfg.Paths.Media)
	}

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

	// Library and media routes — all require authentication.
	libraryHandler := httpapi.NewLibraryHandler(db, authHandler.CSRF)
	mediaHandler := httpapi.NewMediaHandler(db, &library.Store{}, cfg.Paths.Media, mediaAccelPrefix(cfg))

	libRoutes := authHandler.RequireAuth(libraryMux(libraryHandler))
	mux.Handle("/api/v1/libraries", libRoutes)
	mux.Handle("/api/v1/libraries/", libRoutes)
	mux.Handle("/api/v1/works/", libRoutes)
	mux.Handle("/api/v1/editions/", libRoutes)
	mux.Handle("/api/v1/chapters/", libRoutes)
	mux.Handle("/api/v1/assets/", libRoutes)

	mediaRoutes := authHandler.RequireAuth(mediaMux(mediaHandler))
	mux.Handle("/api/v1/media/", mediaRoutes)
	mux.Handle("/api/v1/audio/", mediaRoutes)

	var articleAnnotator annotator.Annotator
	if len(providers) > 0 {
		articleAnnotator = providers[0]
	}
	articleHandler := httpapi.NewArticleHandler(db, authHandler.CSRF, articleAnnotator, mediaStore)
	articleRoutes := authHandler.RequireAuth(articleMux(articleHandler))
	mux.Handle("/api/v1/articles", articleRoutes)
	mux.Handle("/api/v1/articles/", articleRoutes)
	mux.Handle("/api/v1/learning-state", articleRoutes)

	workerService := workers.NewService(db, mediaStore)
	workerHandler := httpapi.NewSpeechWorkerHandler(workerService, authHandler.CSRF)
	ownerWorkerMux := http.NewServeMux()
	ownerWorkerMux.HandleFunc("POST /api/v1/speech-workers/enrollments", workerHandler.ServeOwnerEnrollments)
	ownerWorkerMux.HandleFunc("GET /api/v1/speech-workers", workerHandler.ServeOwnerWorkers)
	ownerWorkerMux.HandleFunc("DELETE /api/v1/speech-workers/{id}", workerHandler.ServeOwnerWorker)
	mux.Handle("/api/v1/speech-workers", authHandler.RequireAuth(ownerWorkerMux))
	mux.Handle("/api/v1/speech-workers/", authHandler.RequireAuth(ownerWorkerMux))

	// These routes intentionally do not use browser-session or CSRF middleware.
	// The worker service authenticates the independent application credential.
	mux.HandleFunc("POST /api/v1/speech-worker/enroll", workerHandler.ServeEnroll)
	mux.HandleFunc("POST /api/v1/speech-worker/lease", workerHandler.ServeLease)
	mux.HandleFunc("POST /api/v1/speech-worker/jobs/{id}/heartbeat", workerHandler.ServeHeartbeat)
	mux.HandleFunc("POST /api/v1/speech-worker/jobs/{id}/complete", workerHandler.ServeComplete)
	mux.HandleFunc("POST /api/v1/speech-worker/jobs/{id}/fail", workerHandler.ServeFail)

	return mux
}

// libraryMux returns a single http.Handler that dispatches library routes based
// on the URL path. All library routes share the same auth wrapper.
func libraryMux(h *httpapi.LibraryHandler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/libraries", h.ServeLibraries)
	mux.HandleFunc("POST /api/v1/libraries", h.ServeLibraries)
	mux.HandleFunc("GET /api/v1/libraries/{id}", h.ServeLibrary)
	mux.HandleFunc("PUT /api/v1/libraries/{id}", h.ServeLibrary)
	mux.HandleFunc("DELETE /api/v1/libraries/{id}", h.ServeLibrary)

	mux.HandleFunc("GET /api/v1/libraries/{id}/works", h.ServeWorksByLibrary)
	mux.HandleFunc("POST /api/v1/libraries/{id}/works", h.ServeWorksByLibrary)

	mux.HandleFunc("GET /api/v1/works/{id}", h.ServeWork)
	mux.HandleFunc("PUT /api/v1/works/{id}", h.ServeWork)
	mux.HandleFunc("DELETE /api/v1/works/{id}", h.ServeWork)

	mux.HandleFunc("GET /api/v1/works/{id}/editions", h.ServeEditionsByWork)
	mux.HandleFunc("POST /api/v1/works/{id}/editions", h.ServeEditionsByWork)

	mux.HandleFunc("GET /api/v1/editions/{id}", h.ServeEdition)
	mux.HandleFunc("PUT /api/v1/editions/{id}", h.ServeEdition)
	mux.HandleFunc("DELETE /api/v1/editions/{id}", h.ServeEdition)

	mux.HandleFunc("GET /api/v1/editions/{id}/chapters", h.ServeChaptersByEdition)
	mux.HandleFunc("POST /api/v1/editions/{id}/chapters", h.ServeChaptersByEdition)

	mux.HandleFunc("GET /api/v1/chapters/{id}", h.ServeChapter)
	mux.HandleFunc("PUT /api/v1/chapters/{id}", h.ServeChapter)
	mux.HandleFunc("DELETE /api/v1/chapters/{id}", h.ServeChapter)

	mux.HandleFunc("GET /api/v1/chapters/{id}/assets", h.ServeAssetsByChapter)
	mux.HandleFunc("POST /api/v1/chapters/{id}/assets", h.ServeAssetsByChapter)

	mux.HandleFunc("GET /api/v1/assets/{id}", h.ServeSourceAsset)
	mux.HandleFunc("PUT /api/v1/assets/{id}", h.ServeSourceAsset)
	mux.HandleFunc("DELETE /api/v1/assets/{id}", h.ServeSourceAsset)

	return mux
}

func mediaMux(h *httpapi.MediaHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/media/{id}", h.ServeMedia)
	mux.HandleFunc("/api/v1/audio/{id}", h.ServeAudio)
	return mux
}

func articleMux(h *httpapi.ArticleHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/articles", h.ServeArticles)
	mux.HandleFunc("/api/v1/articles/{id}", h.ServeArticle)
	mux.HandleFunc("/api/v1/articles/{id}/enrich", h.ServeEnrichQueued)
	mux.HandleFunc("/api/v1/articles/{id}/reanalyze", h.ServeReanalyze)
	mux.HandleFunc("POST /api/v1/articles/{id}/narration", h.ServeGenerateNarration)
	mux.HandleFunc("GET /api/v1/articles/{id}/narration", h.ServeNarration)
	mux.HandleFunc("DELETE /api/v1/articles/{id}/narration", h.ServeClearNarration)
	mux.HandleFunc("/api/v1/learning-state", h.ServeLearningState)
	return mux
}

func mediaAccelPrefix(cfg *config.Config) string {
	if !cfg.MediaRedirect.Enabled {
		return ""
	}
	return cfg.MediaRedirect.Prefix
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
