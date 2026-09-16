// modbus-slave 는 모든 레지스터가 0을 반환하는 Modbus TCP slave 시뮬레이터다.
// 컨테이너 1개 = slave 1개로 동작하는 것을 전제로 한다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"modbus-slave/internal/config"
	"modbus-slave/internal/health"
	"modbus-slave/internal/server"
	"modbus-slave/internal/state"
)

// shutdownGrace 는 SIGTERM 이후 진행 중인 커넥션을 기다리는 시간이다.
// docker stop 기본 대기(10초)보다 짧게 잡아 컨테이너가 강제 종료되지 않게 한다.
const shutdownGrace = 5 * time.Second

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Parse(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "설정 오류: %v\n", err)
		return 2
	}

	if cfg.Healthcheck {
		if err := health.Run(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck 실패: %v\n", err)
			return 1
		}
		return 0
	}

	log := newLogger(cfg.LogLevel)
	srv := server.New(cfg, log)
	if err := srv.Listen(); err != nil {
		log.Error("포트 바인드 실패", "bind", cfg.Bind, "port", cfg.Port, "err", err)
		return 1
	}

	if err := state.Write(cfg.StateFile, newState(cfg, srv.Port())); err != nil {
		// 상태 파일은 healthcheck 편의용이므로 실패해도 서비스는 계속한다.
		log.Debug("상태 파일 기록 실패", "path", cfg.StateFile, "err", err)
	}
	defer state.Remove(cfg.StateFile)

	log.Info("modbus slave 시작",
		"bind", cfg.Bind,
		"port", srv.Port(),
		"registers", cfg.Registers,
		"unit_id", unitIDLabel(cfg),
		"function_codes", cfg.FunctionCodeList(),
		"max_conns", cfg.MaxConns)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	select {
	case err := <-serveErr:
		if err != nil {
			log.Error("서버 중단", "err", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		log.Info("종료 신호 수신, 진행 중인 커넥션 정리", "grace", shutdownGrace.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("유예 시간 내 정리되지 않아 커넥션 강제 종료", "err", err)
		}
		<-serveErr
		log.Info("종료 완료")
		return 0
	}
}

func newState(cfg *config.Config, port int) state.State {
	fcs := make([]int, 0, len(cfg.FunctionCodes))
	for _, fc := range cfg.FunctionCodes {
		fcs = append(fcs, int(fc))
	}
	return state.State{
		PID:           os.Getpid(),
		Bind:          cfg.Bind,
		Port:          port,
		Registers:     cfg.Registers,
		UnitID:        cfg.UnitID,
		AnyUnitID:     cfg.AnyUnitID,
		FunctionCodes: fcs,
		StartedAt:     time.Now().UTC(),
	}
}

func unitIDLabel(cfg *config.Config) string {
	if cfg.AnyUnitID {
		return "any"
	}
	return fmt.Sprintf("%d", cfg.UnitID)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
