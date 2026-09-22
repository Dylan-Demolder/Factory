package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dylan-demolder/factory/internal/web"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdServe(ctx context.Context, args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", envOr("FACTORY_ADDR", "127.0.0.1:7700"), "listen address")
	workspace := fs.String("workspace", envOr("FACTORY_WORKSPACE", filepath.Join(home, "factory-projects")), "directory holding projects")
	cfgFlag := fs.String("config", "", "config file for new projects")
	token := fs.String("token", os.Getenv("FACTORY_TOKEN"), "access token")
	noAuth := fs.Bool("no-auth", false, "disable authentication (only allowed on a loopback address)")
	basePath := fs.String("base-path", envOr("FACTORY_BASE_PATH", ""), "URL prefix, e.g. /factory")
	frame := fs.String("frame-ancestors", envOr("FACTORY_FRAME_ANCESTORS", "'self'"), "CSP frame-ancestors (who may embed the UI)")
	certFile := fs.String("tls-cert", "", "TLS certificate file")
	keyFile := fs.String("tls-key", "", "TLS key file")
	var origins multiFlag
	fs.Var(&origins, "allow-origin", "origin allowed to call the API cross-site (repeatable)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if env := os.Getenv("FACTORY_ALLOW_ORIGINS"); env != "" && len(origins) == 0 {
		for _, o := range strings.Split(env, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
	}
	for _, o := range origins {
		if o == "*" {
			return errors.New("--allow-origin '*' is not allowed; list exact origins like https://example.com")
		}
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("--addr: %w", err)
	}
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	savedTokenNote := ""
	if *noAuth {
		if !loopback {
			return errors.New("--no-auth is only allowed when listening on a loopback address")
		}
		*token = ""
	} else if *token == "" {
		t, path, err := loadOrCreateToken()
		if err != nil {
			return err
		}
		*token = t
		savedTokenNote = path
	}
	if !*noAuth && len(*token) < 16 {
		return errors.New("the access token must be at least 16 characters")
	}
	// Print the token on every start, whichever way it arrived: --token,
	// FACTORY_TOKEN, or the file just written. It used to print only when
	// generated, so a configured install never showed it and the only way to
	// log in was to go read the config by hand.
	if !*noAuth {
		if savedTokenNote != "" {
			fmt.Printf("access token (generated, saved in %s):\n  %s\n\n", savedTokenNote, *token)
		} else {
			fmt.Printf("access token:\n  %s\n\n", *token)
		}
	}

	srv, err := web.New(web.Options{
		Workspace: *workspace, ConfigPath: *cfgFlag, Token: *token, BasePath: *basePath,
		AllowOrigins: origins, FrameAncestors: *frame, Version: version,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	hs := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	scheme := "http"
	if *certFile != "" {
		scheme = "https"
	}
	shown := *addr
	if host == "" || host == "0.0.0.0" || host == "::" {
		shown = "localhost" + strings.TrimPrefix(*addr, host)
	}
	fmt.Printf("factory web interface: %s://%s%s/\nworkspace: %s\n", scheme, shown, strings.TrimRight("/"+strings.Trim(*basePath, "/"), "/"), *workspace)
	if !loopback && *certFile == "" {
		fmt.Println("note: listening beyond localhost without TLS — put it behind an HTTPS reverse proxy (see README).")
	}

	errCh := make(chan error, 1)
	go func() {
		if *certFile != "" {
			errCh <- hs.ListenAndServeTLS(*certFile, *keyFile)
		} else {
			errCh <- hs.ListenAndServe()
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		fmt.Println("\nshutting down (background builds keep running)")
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(shutdown)
	}
}

const version = "0.2.0"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadOrCreateToken() (string, string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, "factory", "token")
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 16 {
		return strings.TrimSpace(string(data)), path, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	t := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return t, path, nil
}
