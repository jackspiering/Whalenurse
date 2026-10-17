// Command whalenurse is a compose-aware self-healer for Docker hosts.
//
// M1 implements `run` (restart-only healing) and `healthcheck`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/jackspiering/whalenurse/internal/api"
	"github.com/jackspiering/whalenurse/internal/clock"
	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/control"
	"github.com/jackspiering/whalenurse/internal/daemon"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/engine"
)

// version is set with -ldflags "-X main.version=...".
var version = ""

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = runCmd(args)
	case "healthcheck":
		err = healthCmd(args)
	case "version":
		fmt.Println("whalenurse", buildVersion())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "whalenurse: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "whalenurse:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: whalenurse [run] [--dry-run]
       whalenurse healthcheck
       whalenurse version

run          watch Docker and heal opted-in containers (the default command)
healthcheck  exit 0 if the running daemon reports healthy (for the image HEALTHCHECK)
`)
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev"
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "log decisions only; change nothing (same as WHALENURSE_DRY_RUN=true)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, warns, err := config.FromEnv(os.Environ())
	if err != nil {
		return err
	}
	if *dry {
		cfg.DryRun = true
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	for _, w := range warns {
		log.Warn("config", "detail", w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cli, err := docker.NewMoby()
	if err != nil {
		return err
	}
	defer cli.Close()
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = cli.Ping(pctx)
	cancel()
	if err != nil {
		return fmt.Errorf("cannot reach the Docker API (is the socket mounted, and is the user in its group?): %w", err)
	}

	selfID := ownContainerID()
	if selfID != "" {
		if me, err := cli.Inspect(ctx, selfID); err == nil && docker.ContainerRef(me.NetworkMode) != "" {
			log.Warn("whalenurse runs with network_mode container:<id>; it would become a dependent of a container it may heal. Do not do this.", "network_mode", me.NetworkMode)
		}
		log.Info("running in a container", "self", docker.ShortID(selfID))
	}

	bus := &engine.Bus{}
	bus.Subscribe(daemon.LogSink(log))

	var actor docker.Actor = cli
	if cfg.DryRun {
		actor = docker.DryRun{Would: func(verb, id string) { log.Info("dry-run: would act", "verb", verb, "id", docker.ShortID(id)) }}
		log.Warn("DRY RUN: decisions are logged, no container is changed")
	}
	d := daemon.New(daemon.Options{
		Config: cfg, Reader: cli, Actor: actor, Clock: clock.Real{}, Bus: bus, Log: log, SelfID: selfID,
	})

	h := api.Handler(d)
	if cfg.ControlSocket != "" {
		go func() {
			if err := control.Serve(ctx, cfg.ControlSocket, h); err != nil {
				log.Error("control socket unavailable; the image healthcheck will fail", "path", cfg.ControlSocket, "error", err.Error())
			}
		}()
	}
	if cfg.HTTPAddr != "" {
		srv := &http.Server{Addr: cfg.HTTPAddr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("http listener failed", "addr", cfg.HTTPAddr, "error", err.Error())
			}
		}()
		go func() { <-ctx.Done(); _ = srv.Close() }()
	}

	if err := d.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped")
	return nil
}

func healthCmd(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _, err := config.FromEnv(os.Environ())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return control.Health(ctx, cfg.ControlSocket)
}

func ownContainerID() string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	return docker.SelfID(f)
}
