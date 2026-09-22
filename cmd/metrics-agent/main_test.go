package main

import (
	"io"
	"strings"
	"testing"
)

func TestFieldName(t *testing.T) {
	tests := map[string]string{
		"modbus-slave-502":  "502",
		"modbus-slave-549":  "549",
		"modbus-slave-5020": "5020",
		"slave-502":         "502",
		"weird-name":        "weird-name",
		"modbus-slave-":     "modbus-slave-",
	}
	for in, want := range tests {
		if got := fieldName(in); got != want {
			t.Errorf("fieldName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildFields(t *testing.T) {
	fields := buildFields(map[string]float64{
		"modbus-slave-503": 0.874,
		"modbus-slave-502": 0.862,
	})
	// 포트 오름차순 + total + ts
	if len(fields) != 8 {
		t.Fatalf("필드 수 %d, want 8: %v", len(fields), fields)
	}
	if fields[0] != "502" || fields[1] != "0.86" {
		t.Errorf("첫 필드 %q=%q, want 502=0.86", fields[0], fields[1])
	}
	if fields[2] != "503" || fields[3] != "0.87" {
		t.Errorf("둘째 필드 %q=%q, want 503=0.87", fields[2], fields[3])
	}
	if fields[4] != "total" || fields[5] != "1.74" {
		t.Errorf("total = %q, want 1.74", fields[5])
	}
	if fields[6] != "ts" || fields[7] == "" {
		t.Errorf("ts 누락: %v", fields[6:])
	}
}

func TestBuildFieldsEmpty(t *testing.T) {
	fields := buildFields(map[string]float64{})
	if len(fields) != 4 || fields[0] != "total" || fields[1] != "0.00" {
		t.Fatalf("빈 입력 결과 %v", fields)
	}
}

func TestParseFlagsRequiresRedisAddr(t *testing.T) {
	if _, err := parseFlags(nil, func(string) string { return "" }, io.Discard); err == nil {
		t.Fatal("--redis-addr 없는데 오류 없음")
	}
}

func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := parseFlags([]string{"--redis-addr", "2mtest.liz.com"}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatalf("오류: %v", err)
	}
	// 포트를 생략하면 기본 Redis 포트를 붙인다.
	if cfg.redisAddr != "2mtest.liz.com:6379" {
		t.Errorf("redisAddr = %q", cfg.redisAddr)
	}
	if cfg.key != defaultKey {
		t.Errorf("key = %q, want %q", cfg.key, defaultKey)
	}
	if cfg.interval != defaultInterval {
		t.Errorf("interval = %s, want %s", cfg.interval, defaultInterval)
	}
	if cfg.prefix != defaultPrefix {
		t.Errorf("prefix = %q", cfg.prefix)
	}
}

func TestParseFlagsEnvAndOverride(t *testing.T) {
	env := map[string]string{"REDIS_ADDR": "a:1", "REDIS_DB": "3", "REDIS_KEY": "k"}
	get := func(s string) string { return env[s] }

	cfg, err := parseFlags(nil, get, io.Discard)
	if err != nil || cfg.redisAddr != "a:1" || cfg.redisDB != 3 || cfg.key != "k" {
		t.Fatalf("환경변수 반영 실패: %+v (%v)", cfg, err)
	}
	cfg, err = parseFlags([]string{"--redis-addr", "b:2"}, get, io.Discard)
	if err != nil || cfg.redisAddr != "b:2" {
		t.Fatalf("CLI 우선 적용 실패: %+v (%v)", cfg, err)
	}
}

func TestParseFlagsRejects(t *testing.T) {
	get := func(string) string { return "" }
	for _, args := range [][]string{
		{"--redis-addr", "a:1", "--redis-db", "-1"},
		{"--redis-addr", "a:1", "--redis-db", "x"},
		{"--redis-addr", "a:1", "--interval", "500ms"},
	} {
		if _, err := parseFlags(args, get, io.Discard); err == nil {
			t.Errorf("parseFlags(%v) 오류 없음", args)
		}
	}
}

func TestTotalOf(t *testing.T) {
	if got := totalOf(map[string]float64{"a": 1.5, "b": 2.5}); got != 4.0 {
		t.Fatalf("totalOf = %v, want 4", got)
	}
	_ = strings.TrimSpace("")
}
