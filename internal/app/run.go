package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/accountprobe"
	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/api"
	"github.com/langrenjh-alt/GROK-GO/internal/apikey"
	"github.com/langrenjh-alt/GROK-GO/internal/buildinfo"
	"github.com/langrenjh-alt/GROK-GO/internal/config"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/configsync"
	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/gateway"
	"github.com/langrenjh-alt/GROK-GO/internal/httpx"
	"github.com/langrenjh-alt/GROK-GO/internal/media"
	"github.com/langrenjh-alt/GROK-GO/internal/persistence"
	"github.com/langrenjh-alt/GROK-GO/internal/requestlog"
	"github.com/langrenjh-alt/GROK-GO/internal/runtimecfg"
	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
	"github.com/langrenjh-alt/GROK-GO/internal/webui"
)

const (
	backgroundOAuthRefreshInterval = 15 * time.Minute
	backgroundOAuthRefreshBefore   = time.Hour
)

func Run(ctx context.Context) error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := newLogger(os.Getenv("GROK_GO_LOG_LEVEL"))
	slog.SetDefault(logger)
	instanceID := runtimeInstanceID(cfg.InstanceID, cfg.HTTP.Address)

	postgres, err := database.OpenPostgres(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer postgres.Close()
	if err := database.Migrate(ctx, postgres); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	redis, err := database.OpenRedis(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer redis.Close()

	cipher, err := security.NewCipher(cfg.Security.EncryptionKey)
	if err != nil {
		return err
	}
	tokens, err := security.NewTokenManager(cfg.Security.TokenPepper)
	if err != nil {
		return err
	}
	totp, err := security.NewTOTP(cfg.Admin.TOTPIssuer)
	if err != nil {
		return err
	}
	repository := store.NewPostgres(postgres)
	authState := admin.NewRedisAuthState(redis, tokens, cfg.Security.SessionTTL)
	auth := admin.NewAuthService(repository, cipher, security.NewDefaultPasswordHasher(), tokens, totp, cfg.Security.SessionTTL, authState)
	management := admin.NewManagementService(repository, repository, repository, repository, repository, cipher, tokens)
	settings := persistence.SettingsStore{Pool: postgres}
	persistedSettings, err := settings.LoadSettings(ctx)
	if err != nil {
		return fmt.Errorf("load runtime settings: %w", err)
	}
	settingDefaults := runtimecfg.Defaults()
	settingDefaults.PublicBaseURL = cfg.HTTP.PublicURL
	settingDefaults.TrustProxyHeaders = cfg.HTTP.TrustProxyHeaders
	settingDefaults.LogRetentionDays = requestLogRetentionDays(os.Getenv("GROK_GO_REQUEST_LOG_RETENTION_DAYS"), settingDefaults.LogRetentionDays)
	settingValues, err := runtimecfg.Resolve(settingDefaults, persistedSettings)
	if err != nil {
		return fmt.Errorf("resolve runtime settings: %w", err)
	}
	runtimeSettings, err := runtimecfg.NewRuntime(settingDefaults, settingValues)
	if err != nil {
		return fmt.Errorf("initialize runtime settings: %w", err)
	}
	// Restart-only values become active here, before their consumers are built.
	cfg.HTTP.PublicURL = settingValues.PublicBaseURL
	cfg.HTTP.TrustProxyHeaders = settingValues.TrustProxyHeaders

	if cfg.Admin.BootstrapEmail != "" && cfg.Admin.BootstrapPassword != "" {
		count, countErr := repository.CountAdmins(ctx)
		if countErr != nil {
			return countErr
		}
		if count == 0 {
			if _, err := auth.Bootstrap(ctx, cfg.Admin.BootstrapEmail, cfg.Admin.BootstrapPassword); err != nil {
				return fmt.Errorf("bootstrap administrator: %w", err)
			}
		}
	}

	accountAdapter := AccountStoreAdapter{Repository: repository, Management: management}
	accountPolicy := accounts.DefaultPolicy()
	if value, ok := persistedSettings["account_scheduling_strategy"].(string); ok && strings.TrimSpace(value) != "" {
		if strategy, parseErr := accounts.ParseStrategy(value); parseErr == nil {
			accountPolicy.Strategy = strategy
		} else {
			logger.Warn("invalid persisted account scheduling strategy", "value", value)
		}
	}
	accountPool := accounts.NewPoolWithCoordinator(accountAdapter, accountPolicy, accounts.NewRedisCoordinator(redis))
	if err := accountPool.Reload(ctx); err != nil {
		return fmt.Errorf("load upstream accounts: %w", err)
	}
	configurationSync, err := configsync.New(configsync.Config{
		Bus: redis, Settings: settings, RuntimeSettings: runtimeSettings, Accounts: accountPool,
		InstanceID: instanceID,
		OnError:    func(err error) { logger.Warn("configuration synchronization failed", "error", err) },
	})
	if err != nil {
		return fmt.Errorf("configure runtime synchronization: %w", err)
	}
	accountPool.SetChangeNotifier(configurationSync)
	modelSource := ModelSourceAdapter{Repository: repository}

	mediaStore, err := media.NewFileStore(filepath.Join(cfg.Media.DataDir, "media"), cfg.Media.MaxBytes)
	if err != nil {
		return fmt.Errorf("open media store: %w", err)
	}
	mediaSigner, err := media.NewSigner(cfg.Security.TokenPepper, cfg.Media.SignedURLTTL)
	if err != nil {
		return err
	}
	remoteMedia := &media.RemoteCache{
		Store: mediaStore, Signer: mediaSigner, PublicBaseURL: cfg.HTTP.PublicURL,
		MaxFetchBytes: cfg.Media.MaxFetchBytes, ImageTTL: cfg.Media.ImageTTL, VideoTTL: cfg.Media.VideoTTL,
	}
	videoStore := persistence.VideoStore{Pool: postgres, Media: mediaStore, OwnerID: instanceID}
	if interrupted, err := videoStore.FailInterruptedVideos(ctx, time.Now().Add(-15*time.Minute)); err != nil {
		return fmt.Errorf("reconcile interrupted video jobs: %w", err)
	} else if interrupted > 0 {
		logger.Warn("marked interrupted video jobs as failed", "count", interrupted)
	}

	oauth := upstream.NewOAuthService(upstream.OAuthConfig{
		AuthorizationURL: cfg.OAuth.AuthorizationURL,
		TokenURL:         cfg.OAuth.TokenURL,
		ClientID:         cfg.OAuth.ClientID,
		RedirectURL:      cfg.OAuth.RedirectURL,
		Scope:            cfg.OAuth.Scope,
	}, nil, redis)
	oauthRefresh := &upstream.RefreshService{
		OAuth: oauth, Store: accountAdapter, Locks: redis,
		Before: backgroundOAuthRefreshBefore, Interval: backgroundOAuthRefreshInterval,
		FailureCooldown: backgroundOAuthRefreshInterval, Concurrency: 5,
		OnAccountsChanged: func(changeCtx context.Context) {
			if err := accountPool.Reload(changeCtx); err != nil {
				logger.Warn("reload account pool after OAuth refresh", "error", err)
			}
			if err := configurationSync.Notify(changeCtx, configevent.ScopeAccounts); err != nil {
				logger.Warn("publish OAuth account refresh", "error", err)
			}
		},
	}
	requestTimeout := func() time.Duration {
		return time.Duration(runtimeSettings.Active().RequestTimeoutSeconds) * time.Second
	}
	upstreamClient := upstream.NewHTTPClient(upstream.HTTPClientConfig{
		BackgroundContext:  ctx,
		RequestTimeoutFunc: requestTimeout,
	})
	accountProbe, err := accountprobe.New(accountprobe.Config{
		Accounts: accountPool,
		Reader:   management,
		Upstream: upstreamClient,
		ProxyURL: management.GetProxyURL,
	})
	if err != nil {
		return fmt.Errorf("configure account probe: %w", err)
	}
	rawGateway := gateway.New(gateway.Config{
		Models:            modelSource,
		Accounts:          accountPool,
		Upstream:          upstreamClient,
		Videos:            videoStore,
		Media:             remoteMedia,
		ProxyURL:          management.GetProxyURL,
		BackgroundContext: ctx,
		MaxBodyBytesFunc: func() int64 {
			return runtimeSettings.Active().MaxRequestBytes
		},
		OnCompletion: func(ctx context.Context, completion gateway.Completion) {
			apikey.ReportCompletion(ctx, apikey.Completion{
				AccountID:    completion.AccountID,
				InputTokens:  completion.Usage.InputTokens,
				OutputTokens: completion.Usage.OutputTokens,
				CachedTokens: completion.Usage.CachedTokens,
				UsageParsed:  completion.UsageParsed,
			})
		},
	})
	requestLogSink := requestlog.NewAsyncSink(management, 8192, 8)
	defer func() {
		flushContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if closeErr := requestLogSink.Close(flushContext); closeErr != nil {
			logger.Warn("request log queue did not flush cleanly", "error", closeErr)
		}
		if dropped := requestLogSink.Dropped(); dropped > 0 {
			logger.Warn("request logs were dropped because the queue was full", "count", dropped)
		}
	}()
	protectedGateway := apikey.Middleware{Auth: management, Counters: redis, RequestTimeoutFunc: requestTimeout}.Handler(
		requestlog.Middleware{Sink: requestLogSink}.Handler(rawGateway),
	)
	protectedGateway = httpx.DynamicConcurrency(func() int {
		return runtimeSettings.Active().MaxConcurrency
	})(protectedGateway)

	mediaAdmin := persistence.MediaAdmin{Store: mediaStore}
	adminHandler := api.NewAdminHandler(api.Config{
		Auth:              auth,
		Audit:             repository,
		Management:        management,
		AdminRepository:   repository,
		Accounts:          accountPool,
		AccountProbe:      accountProbe,
		OAuth:             oauth,
		OAuthRefresh:      oauthRefresh,
		Redis:             redis,
		Gateway:           protectedGateway,
		Settings:          settings,
		Media:             mediaAdmin,
		ProxyChecker:      api.ProxyCheckerFunc(checkProxy),
		BootstrapToken:    cfg.Admin.BootstrapToken,
		SessionCookie:     cfg.Security.CookieName,
		CSRFCookie:        "grok_go_csrf",
		CSRFHeader:        cfg.Security.CSRFHeader,
		CookieDomain:      cfg.Security.CookieDomain,
		CookieSecure:      cfg.Security.CookieSecure,
		TrustedOrigin:     cfg.Security.TrustedOrigin,
		TrustProxyHeaders: cfg.HTTP.TrustProxyHeaders,
		ServiceName:       "GROK-GO",
		RuntimeSettings:   runtimeSettings,
		ConfigChanges:     configurationSync,
	})

	web, err := webui.NewHandler()
	if err != nil {
		return fmt.Errorf("open embedded web UI: %w", err)
	}
	router := chi.NewRouter()
	router.Get("/healthz", httpx.Liveness().ServeHTTP)
	router.Get("/readyz", httpx.Readiness(5*time.Second,
		httpx.Check{Name: "postgres", Run: postgres.Ping},
		httpx.Check{Name: "redis", Run: redis.Health},
	).ServeHTTP)
	router.Get("/meta", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, buildinfo.Current())
	})
	router.Handle("/admin/api", http.StripPrefix("/admin/api", adminHandler))
	router.Handle("/admin/api/*", http.StripPrefix("/admin/api", adminHandler))
	router.Handle("/v1", protectedGateway)
	router.Handle("/v1/*", protectedGateway)
	router.Handle("/media/*", media.DownloadHandler{Store: mediaStore, Signer: mediaSigner})
	router.Handle("/*", web)

	handler := httpx.Chain(router,
		httpx.RequestID,
		httpx.SecurityHeaders,
		httpx.Recover(logger),
		httpx.AccessLog(logger),
		httpx.DynamicCORS(func() string { return runtimeSettings.Active().CORSOrigins }),
	)
	server := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	backgroundCtx, cancelBackground := context.WithCancel(ctx)
	defer cancelBackground()
	go synchronizeAccounts(backgroundCtx, logger, accountPool)
	go refreshOAuth(backgroundCtx, logger, oauthRefresh)
	go maintainSessions(backgroundCtx, logger, auth, management, runtimeSettings)
	go func() {
		if err := configurationSync.Run(backgroundCtx); err != nil && backgroundCtx.Err() == nil {
			logger.Warn("configuration synchronization stopped", "error", err)
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("GROK-GO listening", "address", cfg.HTTP.Address, "version", buildinfo.Version)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
	case serveErr := <-errCh:
		if serveErr != nil {
			return serveErr
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func runtimeInstanceID(configured, address string) string {
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}
	hostname, _ := os.Hostname()
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(hostname)) + "\x00" + strings.TrimSpace(address)))
	return "instance-" + hex.EncodeToString(digest[:12])
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}

func checkProxy(ctx context.Context, proxyURL string) error {
	transport, err := upstream.NewTransport(proxyURL)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.x.ai/v1/models", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 {
		return fmt.Errorf("proxy check returned HTTP %d", response.StatusCode)
	}
	return nil
}

func synchronizeAccounts(ctx context.Context, logger *slog.Logger, pool *accounts.Pool) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := pool.Reload(ctx); err != nil {
				logger.Warn("account pool synchronization failed", "error", err)
			}
		}
	}
}

func refreshOAuth(ctx context.Context, logger *slog.Logger, service *upstream.RefreshService) {
	interval := service.Interval
	if interval <= 0 {
		interval = backgroundOAuthRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := service.RefreshDue(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("OAuth refresh pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func maintainSessions(ctx context.Context, logger *slog.Logger, auth *admin.AuthService, management *admin.ManagementService, settings *runtimecfg.Runtime) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-settings.Changes():
		}
		if _, err := auth.CleanupExpiredSessions(ctx); err != nil {
			logger.Warn("session cleanup failed", "error", err)
		}
		retention := time.Duration(settings.Active().LogRetentionDays) * 24 * time.Hour
		if _, err := management.DeleteRequestLogsBefore(ctx, time.Now().Add(-retention)); err != nil {
			logger.Warn("request log cleanup failed", "error", err)
		}
	}
}

func requestLogRetentionDays(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > 3650 {
		return fallback
	}
	return value
}
