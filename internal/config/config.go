// Package config 는 CLI 플래그와 환경변수에서 설정을 읽고 검증한다.
// 우선순위는 CLI 플래그 > 환경변수 > 기본값이다.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"modbus-slave/internal/modbus"
)

// 기본값.
const (
	DefaultBind          = "0.0.0.0"
	DefaultRegisters     = 1000
	DefaultUnitID        = 1
	DefaultFunctionCodes = "3"
	DefaultMaxConns      = 256
	DefaultLogLevel      = "info"
	DefaultStateFile     = "/run/modbus/state.json"
)

// MaxRegisters 는 16비트 주소 공간 전체다.
const MaxRegisters = 65536

// Config 는 slave 인스턴스 하나의 설정이다.
type Config struct {
	Bind          string
	Port          int
	Registers     int
	UnitID        uint8
	AnyUnitID     bool // --unit-id 0: 모든 Unit ID 수락
	FunctionCodes []byte
	MaxConns      int
	IdleTimeout   time.Duration
	LogLevel      string
	StateFile     string
	Healthcheck   bool
}

// Accepts 는 해당 Unit ID 요청에 응답해야 하는지 반환한다.
func (c *Config) Accepts(unitID uint8) bool {
	return c.AnyUnitID || unitID == c.UnitID
}

// FunctionCodeList 는 로그/오류 메시지용 문자열이다.
func (c *Config) FunctionCodeList() string {
	parts := make([]string, 0, len(c.FunctionCodes))
	for _, fc := range c.FunctionCodes {
		parts = append(parts, fmt.Sprintf("0x%02X", fc))
	}
	return strings.Join(parts, ",")
}

// Parse 는 args 와 getenv 에서 설정을 만든다. 사용법/오류는 out 으로 출력한다.
func Parse(args []string, getenv func(string) string, out io.Writer) (*Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	var envErrs []string
	envStr := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	envInt := func(key string, def int) int {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			envErrs = append(envErrs, fmt.Sprintf("%s=%q (정수가 아님)", key, v))
			return def
		}
		return n
	}
	envDur := func(key string, def time.Duration) time.Duration {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			envErrs = append(envErrs, fmt.Sprintf("%s=%q (기간 형식이 아님, 예: 30s)", key, v))
			return def
		}
		return d
	}

	fs := flag.NewFlagSet("modbus-slave", flag.ContinueOnError)
	fs.SetOutput(out)

	cfg := &Config{}
	var unitID int
	var fcSpec string

	fs.StringVar(&cfg.Bind, "bind", envStr("MODBUS_BIND", DefaultBind), "바인드 주소")
	fs.IntVar(&cfg.Port, "port", envInt("MODBUS_PORT", 0), "리스닝 TCP 포트 (필수)")
	fs.IntVar(&cfg.Registers, "registers", envInt("MODBUS_REGISTERS", DefaultRegisters), "레지스터 개수")
	fs.IntVar(&unitID, "unit-id", envInt("MODBUS_UNIT_ID", DefaultUnitID), "응답할 Unit ID (0 = 전체 수락)")
	fs.StringVar(&fcSpec, "function-code", envStr("MODBUS_FUNCTION_CODE", DefaultFunctionCodes), "활성화할 함수 코드 (쉼표 구분, 10진수 또는 0x 표기)")
	fs.IntVar(&cfg.MaxConns, "max-conns", envInt("MODBUS_MAX_CONNS", DefaultMaxConns), "동시 접속 상한")
	fs.DurationVar(&cfg.IdleTimeout, "idle-timeout", envDur("MODBUS_IDLE_TIMEOUT", 0), "유휴 커넥션 종료 시간 (0 = 무제한)")
	fs.StringVar(&cfg.LogLevel, "log-level", envStr("MODBUS_LOG_LEVEL", DefaultLogLevel), "로그 레벨 (debug|info|warn|error)")
	fs.StringVar(&cfg.StateFile, "state-file", envStr("MODBUS_STATE_FILE", DefaultStateFile), "기동 시 기록할 상태 파일 (healthcheck 가 읽음)")
	fs.BoolVar(&cfg.Healthcheck, "healthcheck", false, "healthcheck 모드로 실행하고 종료")

	fs.Usage = func() {
		fmt.Fprintf(out, "modbus-slave - Modbus TCP slave 시뮬레이터 (모든 레지스터 값 0)\n\n")
		fmt.Fprintf(out, "사용법:\n  modbus-slave --port <포트> [옵션]\n\n옵션:\n")
		fs.PrintDefaults()
		fmt.Fprintf(out, "\n환경변수로도 지정할 수 있습니다: MODBUS_PORT, MODBUS_REGISTERS,\n")
		fmt.Fprintf(out, "MODBUS_UNIT_ID, MODBUS_FUNCTION_CODE, MODBUS_BIND, MODBUS_MAX_CONNS,\n")
		fmt.Fprintf(out, "MODBUS_IDLE_TIMEOUT, MODBUS_LOG_LEVEL, MODBUS_STATE_FILE\n")
		fmt.Fprintf(out, "(CLI 플래그가 환경변수보다 우선합니다.)\n")
	}

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if len(envErrs) > 0 {
		return nil, fmt.Errorf("잘못된 환경변수: %s", strings.Join(envErrs, ", "))
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("알 수 없는 인자: %s", strings.Join(fs.Args(), " "))
	}

	fcs, err := ParseFunctionCodes(fcSpec)
	if err != nil {
		return nil, err
	}
	cfg.FunctionCodes = fcs

	if unitID < 0 || unitID > 255 {
		return nil, fmt.Errorf("--unit-id 는 0~255 여야 함 (받은 값: %d)", unitID)
	}
	cfg.UnitID = uint8(unitID)
	cfg.AnyUnitID = unitID == 0

	if err := cfg.validate(); err != nil {
		fs.Usage()
		return nil, err
	}
	return cfg, nil
}

// validate 는 잘못 설정된 컨테이너가 조용히 살아있지 않도록 기동 전에 값을 검사한다.
func (c *Config) validate() error {
	// healthcheck 모드는 상태 파일에서 포트를 복원할 수 있으므로 --port 를 요구하지 않는다.
	if !c.Healthcheck && c.Port == 0 {
		return errors.New("--port 는 필수입니다")
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("--port 는 1~65535 여야 함 (받은 값: %d)", c.Port)
	}
	if c.Registers < 1 || c.Registers > MaxRegisters {
		return fmt.Errorf("--registers 는 1~%d 여야 함 (받은 값: %d)", MaxRegisters, c.Registers)
	}
	if c.MaxConns < 1 {
		return fmt.Errorf("--max-conns 는 1 이상이어야 함 (받은 값: %d)", c.MaxConns)
	}
	if c.IdleTimeout < 0 {
		return fmt.Errorf("--idle-timeout 은 음수일 수 없음 (받은 값: %s)", c.IdleTimeout)
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("--log-level 은 debug|info|warn|error 중 하나여야 함 (받은 값: %q)", c.LogLevel)
	}
	return nil
}

// ParseFunctionCodes 는 "3", "0x03,0x04", "3, 4" 같은 표기를 함수 코드 목록으로 바꾼다.
// 구현되지 않은 코드는 오설정을 즉시 드러내기 위해 오류로 처리한다.
func ParseFunctionCodes(spec string) ([]byte, error) {
	var out []byte
	seen := make(map[byte]bool)
	for _, part := range strings.Split(spec, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 0, 8)
		if err != nil {
			return nil, fmt.Errorf("함수 코드 %q 를 해석할 수 없음 (10진수 또는 0x 표기, 0~255)", p)
		}
		fc := byte(n)
		if !modbus.IsImplemented(fc) {
			return nil, fmt.Errorf("함수 코드 0x%02X 는 아직 구현되지 않음 (지원: %s)", fc, modbus.ImplementedList())
		}
		if seen[fc] {
			continue
		}
		seen[fc] = true
		out = append(out, fc)
	}
	if len(out) == 0 {
		return nil, errors.New("--function-code 에 최소 1개의 함수 코드가 필요함")
	}
	return out, nil
}
