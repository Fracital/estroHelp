package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"elichat/internal/api"
	"elichat/internal/auth"
	"elichat/internal/billing"
	"elichat/internal/db"
	"elichat/internal/provider"
	"elichat/internal/server"
	"elichat/internal/zai"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

func main() {
	godotenv.Load()
	level := slog.LevelInfo
	if envBool("DEBUG", false) {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, env("DATABASE_URL", "postgres://postgres@localhost:5432/elichat"))
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	if admin := os.Getenv("ADMIN_USERNAME"); admin != "" {
		if tag, _ := database.Exec(ctx, `UPDATE users SET admin=TRUE WHERE lower(username)=lower($1) AND NOT admin`, admin); tag.RowsAffected() > 0 {
			slog.Info("granted admin", "username", admin)
		}
	}

	store := &auth.Store{DB: database}
	store.StartCleanup(ctx)
	requireVerified := envBool("REQUIRE_VERIFIED_EMAIL", true)

	providers := map[string]provider.Provider{}
	if key := os.Getenv("CLIPROXY_API_KEY"); key != "" {
		providers["cliproxy"] = provider.NewCliProxy(env("CLIPROXY_BASE_URL", "https://chat.eli.gift/cpa"), key)
	} else {
		slog.Warn("CLIPROXY_API_KEY unset: Kali Gamma and Beta are disabled")
	}
	if !envBool("ZAI_DISABLED", false) {
		providers["zai"] = provider.Zai{}
		zai.SetDB(database)
		zai.Prewarm()
	}

	chainTTL, err := time.ParseDuration(env("RESPONSES_CHAIN_TTL", "30m"))
	if err != nil {
		slog.Warn("RESPONSES_CHAIN_TTL invalid; using 30m", "err", err)
	}
	slog.Info("responses chain", "ttl", api.SetChainTTL(chainTTL))

	dailyLimit, _ := strconv.Atoi(env("DAILY_REQUEST_LIMIT", "300"))
	listenAddr := env("HOST", "127.0.0.1") + ":" + env("PORT", "3000")
	selfHosts := []string{listenAddr, "localhost:" + env("PORT", "3000"), "127.0.0.1:" + env("PORT", "3000")}
	if public := os.Getenv("PUBLIC_HOST"); public != "" {
		selfHosts = append(selfHosts, public, public+":443", public+":80")
	}
	handler := server.New(server.Deps{
		SelfHosts:  selfHosts,
		Store:      store,
		Auth:       &auth.Handlers{Store: store, Mailer: auth.MailerFromEnv(), RequireVerifiedEmail: requireVerified},
		Admin:      &auth.Admin{Store: store},
		Billing:    &billing.Handlers{DB: database, Store: store},
		Chat:       &api.Chat{DB: database, Store: store, Providers: providers, DailyRequestLimit: dailyLimit, RequireVerifiedEmail: requireVerified},
		ExtHandler: zai.ExtHandler(),
		StaticDir:  env("STATIC_DIR", "frontend/dist"),
	})

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("listening", "addr", srv.Addr, "providers", len(providers))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
