// Command whalenurse is a compose-aware self-healer for Docker hosts.
//
// `run` heals opted-in containers; `pause`, `resume`, `unsuspend` and `status`
// talk to the running daemon over its unix control socket; `healthcheck` is the
// image's HEALTHCHECK.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jackspiering/whalenurse/internal/api"
	"github.com/jackspiering/whalenurse/internal/clock"
	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/control"
	"github.com/jackspiering/whalenurse/internal/daemon"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/engine"
	"github.com/jackspiering/whalenurse/internal/store"
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
	case "pause", "resume", "unsuspend":
		err = scopeCmd(cmd, args)
	case "status":
		err = statusCmd(args)
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
       whalenurse pause <project>[/<service>] [--for 2h]
       whalenurse resume <project>[/<service>]
       whalenurse unsuspend <project>[/<service>]
       whalenurse status [--json]
       whalenurse healthcheck
       whalenurse version

run          watch Docker and heal opted-in containers (the default command)
pause        stop all actions for a project or service; --for takes 30m, 2h, 1d ... (default 2h; 0 = until resume)
resume       lift a pause
unsuspend    lift SUSPENDED so the ladder may run again
status       show incidents, SUSPENDED and PAUSED containers, flap counts and the rate-limit budget
healthcheck  exit 0 if the running daemon reports healthy (for the image HEALTHCHECK)

pause, resume, unsuspend and status talk to the running daemon over its control socket
(WHALENURSE_CONTROL_SOCKET, default /run/whalenurse.sock): run them with docker exec.
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

	// State lives in SQLite under WHALENURSE_DATA_DIR. A dry run stores nothing: its
	// incidents and SUSPENDED states are not real, and must not be there for the next
	// real run to find.
	opts := daemon.Options{Config: cfg, Reader: cli, Clock: clock.Real{}, Bus: bus, Log: log, SelfID: selfID}
	if cfg.DryRun {
		log.Info("dry run: state is kept in memory only and nothing is written under the data directory")
	} else {
		st, err := store.Open(cfg.DataDir, log)
		if err != nil {
			return fmt.Errorf("cannot open the state store in %s (is WHALENURSE_DATA_DIR a writable volume?): %w", cfg.DataDir, err)
		}
		defer st.Close()
		snap, err := st.Load(time.Now())
		if err != nil {
			return fmt.Errorf("cannot read the state store: %w", err)
		}
		log.Info("state store opened", "dir", cfg.DataDir, "keys", len(snap.Keys), "pauses", len(snap.Pauses))
		opts.Persist, opts.Snapshot, opts.Meta = st, snap, st
		go pruneLoop(ctx, st, cfg, log)
	}

	var actor docker.Actor = cli
	if cfg.DryRun {
		actor = docker.DryRun{Would: func(verb, id string) { log.Info("dry-run: would act", "verb", verb, "id", docker.ShortID(id)) }}
		log.Warn("DRY RUN: decisions are logged, no container is changed")
	}
	opts.Actor = actor
	d := daemon.New(opts)

	tcpHandler, sockHandler := handlers(d)
	if cfg.ControlSocket != "" {
		go func() {
			if err := control.Serve(ctx, cfg.ControlSocket, sockHandler); err != nil {
				log.Error("control socket unavailable; the image healthcheck will fail", "path", cfg.ControlSocket, "error", err.Error())
			}
		}()
	}
	if cfg.HTTPAddr != "" {
		srv := &http.Server{Addr: cfg.HTTPAddr, Handler: tcpHandler, ReadHeaderTimeout: 5 * time.Second}
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

// pruneLoop applies WHALENURSE_HISTORY_RETENTION once an hour.
func pruneLoop(ctx context.Context, st *store.Store, cfg config.Global, log *slog.Logger) {
	prune := func() {
		if n, err := st.Prune(time.Now(), cfg.HistoryRetention, cfg.FlapWindow); err != nil {
			log.Error("store prune failed", "error", err.Error())
		} else if n > 0 {
			log.Info("pruned old incident history", "incidents", n, "retention", cfg.HistoryRetention.String())
		}
	}
	prune()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}

// splitArgs parses flags that may come before or after the positional arguments
// (`pause p/web --for 2h`), which flag.Parse alone does not.
func splitArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func socketPath() (string, error) {
	cfg, _, err := config.FromEnv(os.Environ())
	if err != nil {
		return "", err
	}
	return cfg.ControlSocket, nil
}

func scopeCmd(name string, args []string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var dur *string
	if name == "pause" {
		dur = pauseFor(fs)
	}
	pos, err := splitArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: whalenurse %s <project>[/<service>]%s", name, map[bool]string{true: " [--for 2h]", false: ""}[name == "pause"])
	}
	sock, err := socketPath()
	if err != nil {
		return err
	}
	req := control.Request{Scope: pos[0]}
	if dur != nil {
		req.For = *dur
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := control.Command(ctx, sock, name, req)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the raw JSON")
	if _, err := splitArgs(fs, args); err != nil {
		return err
	}
	sock, err := socketPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, body, err := control.Do(ctx, sock, "GET", "/v1/status", nil)
	if err != nil {
		return fmt.Errorf("cannot reach the whalenurse daemon on %s (is it running, and are you in the same container?): %w", sock, err)
	}
	if code != 200 {
		return fmt.Errorf("status: HTTP %d: %.200s", code, body)
	}
	if *asJSON {
		fmt.Println(strings.TrimSpace(string(body)))
		return nil
	}
	var st daemon.Status
	if err := json.Unmarshal(body, &st); err != nil {
		return err
	}
	printStatus(os.Stdout, st)
	return nil
}

func printStatus(w io.Writer, st daemon.Status) {
	flags := []string{}
	if st.DryRun {
		flags = append(flags, "DRY RUN (nothing is stored or changed)")
	}
	if !st.Healthy {
		flags = append(flags, "UNHEALTHY: "+st.HealthDetail)
	}
	if !st.EventsUp {
		flags = append(flags, "docker events stream down")
	}
	if !st.Settled {
		flags = append(flags, "still in the startup settle period")
	}
	fmt.Fprintf(w, "host %s, mode %s", st.Host, st.Mode)
	if len(flags) > 0 {
		fmt.Fprintf(w, " [%s]", strings.Join(flags, "; "))
	}
	fmt.Fprintln(w)
	e := st.Engine
	rate := fmt.Sprintf("%d actions in the last hour", e.ActionsLastH)
	if e.RateLimit > 0 {
		rate += fmt.Sprintf(" (limit %d)", e.RateLimit)
	}
	fmt.Fprintln(w, rate)
	if !e.RestoreUntil.IsZero() {
		fmt.Fprintf(w, "restore window open until %s: starts are restores, kills set no marker\n", e.RestoreUntil.Local().Format(time.TimeOnly))
	}
	if len(e.Pauses) > 0 {
		fmt.Fprintln(w, "\nPAUSED")
		for _, p := range e.Pauses {
			until := "until resume"
			if !p.Until.IsZero() {
				until = "until " + p.Until.Local().Format(time.RFC1123)
			}
			fmt.Fprintf(w, "  %s  %s\n", p.Scope, until)
		}
	}
	if len(e.Keys) == 0 {
		fmt.Fprintln(w, "\nno container has an incident, a flap count or a marker")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tSTATE\tDETAIL\tFLAPS")
	for _, k := range e.Keys {
		var detail []string
		if k.Class != "" {
			detail = append(detail, k.Class)
		}
		if k.SuspendReason != "" {
			detail = append(detail, "suspended: "+k.SuspendReason)
		}
		if k.Rung > 0 {
			detail = append(detail, fmt.Sprintf("rung %d done", k.Rung))
		}
		if !k.CooldownUntil.IsZero() && k.CooldownUntil.After(time.Now()) {
			detail = append(detail, "cooldown until "+k.CooldownUntil.Local().Format(time.TimeOnly))
		}
		if !k.PausedUntil.IsZero() {
			detail = append(detail, "paused until "+k.PausedUntil.Local().Format(time.TimeOnly))
		}
		if k.ManualStop {
			detail = append(detail, "manual-stop marker")
		}
		if k.LoopIncreases > 0 {
			detail = append(detail, fmt.Sprintf("%d restarts counted", k.LoopIncreases))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", k.Key, k.State, strings.Join(detail, ", "), k.Flaps)
	}
	tw.Flush()
}

// daemonLike is what the two listeners need from the daemon.
type daemonLike interface {
	api.Checker
	control.Backend
}

// handlers returns the handler for the TCP listener and the one for the unix
// socket. The TCP one is read-only (/healthz); only the socket can pause,
// resume or unsuspend, because it is reachable only by someone who can exec
// into the container (plan §2: no network-exposed write endpoints in v1).
func handlers(d daemonLike) (tcp, sock http.Handler) {
	tcp = api.Handler(d)
	sock = control.Mux(tcp, d, func(v string) (time.Duration, error) { return daemon.ParseFor(v, config.ParseDuration) })
	return tcp, sock
}

func pauseFor(fs *flag.FlagSet) *string {
	return fs.String("for", "", "how long to pause: 30m, 2h, 1d ... (default 2h; 0 = until resume)")
}
