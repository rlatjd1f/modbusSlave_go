package config

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func mustParse(t *testing.T, args []string, e map[string]string) *Config {
	t.Helper()
	cfg, err := Parse(args, env(e), io.Discard)
	if err != nil {
		t.Fatalf("Parse(%v, %v) 오류: %v", args, e, err)
	}
	return cfg
}

func TestDefaults(t *testing.T) {
	cfg := mustParse(t, []string{"--port", "5020"}, nil)

	if cfg.Port != 5020 {
		t.Errorf("Port = %d, want 5020", cfg.Port)
	}
	if cfg.Registers != DefaultRegisters {
		t.Errorf("Registers = %d, want %d", cfg.Registers, DefaultRegisters)
	}
	if cfg.UnitID != DefaultUnitID || cfg.AnyUnitID {
		t.Errorf("UnitID = %d any=%v, want %d any=false", cfg.UnitID, cfg.AnyUnitID, DefaultUnitID)
	}
	if len(cfg.FunctionCodes) != 1 || cfg.FunctionCodes[0] != 0x03 {
		t.Errorf("FunctionCodes = % X, want [03]", cfg.FunctionCodes)
	}
	if cfg.Bind != DefaultBind || cfg.MaxConns != DefaultMaxConns || cfg.LogLevel != DefaultLogLevel {
		t.Errorf("기본값 불일치: %+v", cfg)
	}
	if cfg.IdleTimeout != 0 {
		t.Errorf("IdleTimeout = %s, want 0", cfg.IdleTimeout)
	}
}

func TestPortIsRequired(t *testing.T) {
	_, err := Parse(nil, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("오류 = %v, want --port 필수 오류", err)
	}
}

func TestHealthcheckDoesNotRequirePort(t *testing.T) {
	cfg := mustParse(t, []string{"--healthcheck"}, nil)
	if !cfg.Healthcheck || cfg.Port != 0 {
		t.Fatalf("healthcheck=%v port=%d", cfg.Healthcheck, cfg.Port)
	}
}

func TestEnvFallback(t *testing.T) {
	cfg := mustParse(t, nil, map[string]string{
		"MODBUS_PORT":          "5021",
		"MODBUS_REGISTERS":     "200",
		"MODBUS_UNIT_ID":       "7",
		"MODBUS_FUNCTION_CODE": "0x04",
		"MODBUS_BIND":          "127.0.0.1",
		"MODBUS_MAX_CONNS":     "8",
		"MODBUS_IDLE_TIMEOUT":  "30s",
		"MODBUS_LOG_LEVEL":     "debug",
	})
	if cfg.Port != 5021 || cfg.Registers != 200 || cfg.UnitID != 7 ||
		cfg.Bind != "127.0.0.1" || cfg.MaxConns != 8 ||
		cfg.IdleTimeout != 30*time.Second || cfg.LogLevel != "debug" {
		t.Fatalf("환경변수 반영 실패: %+v", cfg)
	}
	if len(cfg.FunctionCodes) != 1 || cfg.FunctionCodes[0] != 0x04 {
		t.Fatalf("FunctionCodes = % X, want [04]", cfg.FunctionCodes)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	cfg := mustParse(t, []string{"--port", "6000", "--registers", "50", "--unit-id", "9"},
		map[string]string{"MODBUS_PORT": "5021", "MODBUS_REGISTERS": "200", "MODBUS_UNIT_ID": "7"})
	if cfg.Port != 6000 || cfg.Registers != 50 || cfg.UnitID != 9 {
		t.Fatalf("CLI 우선 적용 실패: %+v", cfg)
	}
}

func TestInvalidEnvIsRejected(t *testing.T) {
	_, err := Parse([]string{"--port", "5020"}, env(map[string]string{"MODBUS_REGISTERS": "많이"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "MODBUS_REGISTERS") {
		t.Fatalf("오류 = %v, want MODBUS_REGISTERS 오류", err)
	}
}

func TestUnitIDZeroMeansAny(t *testing.T) {
	cfg := mustParse(t, []string{"--port", "5020", "--unit-id", "0"}, nil)
	if !cfg.AnyUnitID {
		t.Fatal("--unit-id 0 은 전체 수락이어야 함")
	}
	for _, id := range []uint8{0, 1, 42, 255} {
		if !cfg.Accepts(id) {
			t.Fatalf("Accepts(%d) = false, want true", id)
		}
	}
}

func TestAcceptsOnlyConfiguredUnitID(t *testing.T) {
	cfg := mustParse(t, []string{"--port", "5020"}, nil)
	if !cfg.Accepts(1) {
		t.Fatal("Accepts(1) = false, want true")
	}
	for _, id := range []uint8{0, 2, 255} {
		if cfg.Accepts(id) {
			t.Fatalf("Accepts(%d) = true, want false", id)
		}
	}
}

func TestParseFunctionCodes(t *testing.T) {
	tests := []struct {
		spec string
		want []byte
	}{
		{"3", []byte{0x03}},
		{"0x03", []byte{0x03}},
		{"4", []byte{0x04}},
		{"3,4", []byte{0x03, 0x04}},
		{" 3 , 4 ", []byte{0x03, 0x04}},
		{"0x03,0x04", []byte{0x03, 0x04}},
		{"3,3,4", []byte{0x03, 0x04}}, // 중복 제거
		{"4,3", []byte{0x04, 0x03}},   // 입력 순서 유지
	}
	for _, tt := range tests {
		got, err := ParseFunctionCodes(tt.spec)
		if err != nil {
			t.Errorf("ParseFunctionCodes(%q) 오류: %v", tt.spec, err)
			continue
		}
		if string(got) != string(tt.want) {
			t.Errorf("ParseFunctionCodes(%q) = % X, want % X", tt.spec, got, tt.want)
		}
	}
}

func TestParseFunctionCodesRejects(t *testing.T) {
	for _, spec := range []string{
		"",       // 빈 값
		",",      // 실질적으로 빈 값
		"abc",    // 숫자가 아님
		"999",    // 8비트 초과
		"-1",     // 음수
		"0x2B",   // 미구현 (Read Device Identification)
		"1",      // 미구현 (Read Coils, 2차 범위)
		"16",     // 미구현 (Write Multiple Registers, 2차 범위)
		"3,0x2B", // 일부만 유효해도 실패
	} {
		if _, err := ParseFunctionCodes(spec); err == nil {
			t.Errorf("ParseFunctionCodes(%q) 오류 없음, want 오류", spec)
		}
	}
}

func TestRangeValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"포트 초과", []string{"--port", "70000"}},
		{"레지스터 0", []string{"--port", "5020", "--registers", "0"}},
		{"레지스터 초과", []string{"--port", "5020", "--registers", "65537"}},
		{"unit-id 초과", []string{"--port", "5020", "--unit-id", "256"}},
		{"unit-id 음수", []string{"--port", "5020", "--unit-id", "-1"}},
		{"max-conns 0", []string{"--port", "5020", "--max-conns", "0"}},
		{"잘못된 로그 레벨", []string{"--port", "5020", "--log-level", "verbose"}},
		{"알 수 없는 인자", []string{"--port", "5020", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.args, nil, io.Discard); err == nil {
				t.Fatalf("Parse(%v) 오류 없음, want 오류", tt.args)
			}
		})
	}
}

func TestRegisterUpperBoundIsAccepted(t *testing.T) {
	cfg := mustParse(t, []string{"--port", "5020", "--registers", "65536"}, nil)
	if cfg.Registers != MaxRegisters {
		t.Fatalf("Registers = %d, want %d", cfg.Registers, MaxRegisters)
	}
}

func TestHelpReturnsErrHelp(t *testing.T) {
	_, err := Parse([]string{"-h"}, nil, io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("오류 = %v, want flag.ErrHelp", err)
	}
}
