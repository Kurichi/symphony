package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/Kurichi/symphony/internal/orchestrator"
)

// Config holds HTTP server configuration.
type Config struct {
	Host string
	Port int
}

// Server is the optional HTTP status/API server.
type Server struct {
	echo         *echo.Echo
	config       Config
	orchestrator *orchestrator.Orchestrator
	listener     net.Listener
}

// New creates a new HTTP server.
func New(cfg Config, orch *orchestrator.Orchestrator) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Middleware
	e.Use(middleware.Recover())
	e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
		Format: "${time_rfc3339} ${method} ${uri} ${status} ${latency_human}\n",
	}))

	s := &Server{
		echo:         e,
		config:       cfg,
		orchestrator: orch,
	}

	// Register routes
	s.registerRoutes()

	return s
}

// Start begins listening. Returns the bound address.
func (s *Server) Start() (string, error) {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listen %s: %w", addr, err)
	}
	s.listener = ln

	boundAddr := ln.Addr().String()
	slog.Info("HTTP server listening", "address", boundAddr)

	go func() {
		if err := s.echo.Server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	return boundAddr, nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}

func (s *Server) registerRoutes() {
	// Health check
	s.echo.GET("/health", s.handleHealth)

	// API endpoints
	api := s.echo.Group("/api")
	api.GET("/status", s.handleStatus)
	api.POST("/refresh", s.handleRefresh)

	// Dashboard
	s.echo.GET("/", s.handleDashboard)
}

func (s *Server) handleHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(c echo.Context) error {
	snapshot, err := s.orchestrator.RequestSnapshot(15 * time.Second)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, snapshot)
}

func (s *Server) handleRefresh(c echo.Context) error {
	s.orchestrator.RequestRefresh()
	return c.JSON(http.StatusOK, map[string]any{
		"queued":       true,
		"requested_at": time.Now().UTC(),
		"operations":   []string{"poll", "reconcile"},
	})
}
