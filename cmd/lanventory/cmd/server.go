package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"

	"github.com/lan-dot-party/lanventory/internal/api"
	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/web"
	"github.com/lan-dot-party/lanventory/pkg/version"
)

func init() {
	rootCmd.AddCommand(serverCmd)
}

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the web UI",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		e, err := open(ctx)
		if err != nil {
			return err
		}
		defer e.close()
		log := e.log
		log.Info("starting lanventory", "version", version.GetVersion(), "listen", e.cfg.Listen, "public_url", e.cfg.PublicURL)
		if !e.cfg.SecureCookies() && e.cfg.TLS.CertFile == "" {
			log.Warn("public_url is not https: passwords travel unencrypted between browser and LANventory")
		}

		a := app.New(e.cfg, e.db, e.key, log)
		ui, err := web.New(a)
		if err != nil {
			return err
		}

		r := chi.NewRouter()
		r.Use(middleware.Recoverer, a.RequestLog, middleware.GetHead)
		r.Get("/health", ui.Health)
		r.Get("/llms.txt", ui.LLMs)
		// The REST API lives outside the web UI router but behind the same
		// middlewares, so browser sessions work for it.
		r.With(a.SecurityHeaders, a.SessionMiddleware).Mount("/api", api.New(a).Routes())
		r.With(a.SecurityHeaders, a.SessionMiddleware).Mount("/", ui.Routes())

		srv := &http.Server{
			Addr:              e.cfg.Listen,
			Handler:           r,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
		}
		errc := make(chan error, 1)
		go func() {
			if e.cfg.TLS.CertFile != "" {
				errc <- srv.ListenAndServeTLS(e.cfg.ResolvePath(e.cfg.TLS.CertFile), e.cfg.ResolvePath(e.cfg.TLS.KeyFile))
			} else {
				errc <- srv.ListenAndServe()
			}
		}()
		log.Info("http listening", "addr", e.cfg.Listen)
		select {
		case err := <-errc:
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case <-ctx.Done():
		}
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	},
}
