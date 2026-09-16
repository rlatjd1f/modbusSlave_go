package health

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"modbus-slave/internal/config"
	"modbus-slave/internal/modbus"
	"modbus-slave/internal/server"
	"modbus-slave/internal/state"
)

func startServer(t *testing.T, cfg *config.Config) int {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := server.New(cfg, log)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-done
	})
	return srv.Port()
}

func baseConfig() *config.Config {
	return &config.Config{
		Bind:          "127.0.0.1",
		Port:          0,
		Registers:     1000,
		UnitID:        1,
		FunctionCodes: []byte{modbus.FCReadHoldingRegisters},
		MaxConns:      8,
		LogLevel:      "error",
	}
}

func TestHealthcheckPasses(t *testing.T) {
	cfg := baseConfig()
	port := startServer(t, cfg)

	check := baseConfig()
	check.Port = port
	if err := Run(check); err != nil {
		t.Fatalf("healthcheck 실패: %v", err)
	}
}

// 서버가 FC04만 활성인데 healthcheck 가 FC03 을 쓰면 영구 unhealthy 가 된다.
// 설정된 함수 코드를 따라가는지 확인한다.
func TestHealthcheckFollowsConfiguredFunctionCode(t *testing.T) {
	cfg := baseConfig()
	cfg.FunctionCodes = []byte{modbus.FCReadInputRegisters}
	port := startServer(t, cfg)

	check := baseConfig()
	check.Port = port
	check.FunctionCodes = []byte{modbus.FCReadInputRegisters}
	if err := Run(check); err != nil {
		t.Fatalf("FC04 인스턴스 healthcheck 실패: %v", err)
	}

	// 반대로 FC03 으로 물으면 예외 응답을 받아 실패해야 한다.
	wrong := baseConfig()
	wrong.Port = port
	if err := Run(wrong); err == nil {
		t.Fatal("비활성 함수 코드로 물었는데 healthcheck 가 통과함")
	}
}

func TestHealthcheckFollowsConfiguredUnitID(t *testing.T) {
	cfg := baseConfig()
	cfg.UnitID = 9
	port := startServer(t, cfg)

	check := baseConfig()
	check.Port = port
	check.UnitID = 9
	if err := Run(check); err != nil {
		t.Fatalf("Unit ID 9 healthcheck 실패: %v", err)
	}
}

// 컨테이너 HEALTHCHECK 는 CMD 인자를 볼 수 없으므로 상태 파일로 설정을 복원한다.
func TestHealthcheckRecoversConfigFromStateFile(t *testing.T) {
	cfg := baseConfig()
	cfg.UnitID = 5
	cfg.FunctionCodes = []byte{modbus.FCReadInputRegisters}
	port := startServer(t, cfg)

	path := filepath.Join(t.TempDir(), "state.json")
	if err := state.Write(path, state.State{
		Bind:          "127.0.0.1",
		Port:          port,
		Registers:     1000,
		UnitID:        5,
		FunctionCodes: []int{int(modbus.FCReadInputRegisters)},
	}); err != nil {
		t.Fatalf("상태 파일 기록: %v", err)
	}

	// --port 없이 상태 파일만으로 동작해야 한다.
	check := baseConfig()
	check.StateFile = path
	if err := Run(check); err != nil {
		t.Fatalf("상태 파일 기반 healthcheck 실패: %v", err)
	}
}

func TestHealthcheckFailsWhenNothingListening(t *testing.T) {
	cfg := baseConfig()
	cfg.Port = 1 // 특권 포트, 리스닝 없음
	if err := Run(cfg); err == nil {
		t.Fatal("리스너가 없는데 healthcheck 가 통과함")
	}
}

func TestHealthcheckFailsWithoutPortOrStateFile(t *testing.T) {
	cfg := baseConfig()
	cfg.StateFile = filepath.Join(t.TempDir(), "missing.json")
	if err := Run(cfg); err == nil {
		t.Fatal("포트를 알 수 없는데 healthcheck 가 통과함")
	}
}

func TestDialHost(t *testing.T) {
	tests := map[string]string{
		"":          "127.0.0.1",
		"0.0.0.0":   "127.0.0.1",
		"::":        "::1",
		"127.0.0.1": "127.0.0.1",
		"10.0.0.5":  "10.0.0.5",
	}
	for in, want := range tests {
		if got := dialHost(in); got != want {
			t.Errorf("dialHost(%q) = %q, want %q", in, got, want)
		}
	}
}
