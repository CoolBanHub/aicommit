package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/CoolBanHub/aicommit/internal/app"
)

type Options struct {
	Addr       string
	ConfigPath string
}

func Run(ctx context.Context, opts Options) error {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8686"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /commit", func(w http.ResponseWriter, r *http.Request) {
		var req app.CommitOptions
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.ConfigPath == "" {
			req.ConfigPath = opts.ConfigPath
		}
		result, err := app.RunCommit(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /push", func(w http.ResponseWriter, r *http.Request) {
		var req app.PushOptions
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := app.RunPush(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("aicommit listening on http://%s\n", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
