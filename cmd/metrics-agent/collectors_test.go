package main

import (
	"strings"
	"testing"
)

const sample3 = `
# 주석
bas       502-505   collector-1   172.31.53.105
bms       506-509   collector-1   172.31.53.105

rosie     546-549   collector-4   172.31.62.55
단일      600       collector-9
`

func TestParseCollectors(t *testing.T) {
	m, err := parseCollectors(strings.NewReader(sample3), "test")
	if err != nil {
		t.Fatalf("파싱 오류: %v", err)
	}
	// 4+4+4+1
	if len(m) != 13 {
		t.Fatalf("포트 수 %d, want 13", len(m))
	}
	tests := map[string]struct{ col, srv, ip string }{
		"502": {"bas", "collector-1", "172.31.53.105"},
		"505": {"bas", "collector-1", "172.31.53.105"},
		"506": {"bms", "collector-1", "172.31.53.105"},
		"549": {"rosie", "collector-4", "172.31.62.55"},
		"600": {"단일", "collector-9", ""},
	}
	for port, want := range tests {
		got, ok := m.lookup(port)
		if !ok {
			t.Errorf("lookup(%s) 없음", port)
			continue
		}
		if got.Collector != want.col || got.Server != want.srv || got.ServerIP != want.ip {
			t.Errorf("lookup(%s) = %+v, want %+v", port, got, want)
		}
	}
	for _, port := range []string{"501", "510", "550", "abc", ""} {
		if _, ok := m.lookup(port); ok {
			t.Errorf("lookup(%s) 가 매칭됨, want 없음", port)
		}
	}
}

func TestLookupOnNilMap(t *testing.T) {
	var m collectorMap
	if _, ok := m.lookup("502"); ok {
		t.Fatal("nil 맵인데 매칭됨")
	}
}

func TestParseCollectorsRejects(t *testing.T) {
	bad := []string{
		"bas",                      // 포트 범위 없음
		"bas 502-abc",              // 숫자 아님
		"bas 505-502",              // 시작이 끝보다 큼
		"bas 0-5",                  // 범위 밖
		"bas 502-505\nbms 504-509", // 포트 중복
	}
	for _, in := range bad {
		if _, err := parseCollectors(strings.NewReader(in), "test"); err == nil {
			t.Errorf("parseCollectors(%q) 오류 없음", in)
		}
	}
}

func TestParseCollectorsEmpty(t *testing.T) {
	m, err := parseCollectors(strings.NewReader("# 주석만\n\n"), "test")
	if err != nil {
		t.Fatalf("오류: %v", err)
	}
	if m != nil {
		t.Fatalf("빈 파일인데 %v", m)
	}
}

func TestExporterLabels(t *testing.T) {
	m, _ := parseCollectors(strings.NewReader(sample3), "test")
	e := newExporter(m)

	got := e.labels("modbus-slave-502")
	for _, want := range []string{`port="502"`, `container="modbus-slave-502"`,
		`collector="bas"`, `server="collector-1"`, `server_ip="172.31.53.105"`} {
		if !strings.Contains(got, want) {
			t.Errorf("labels 에 %s 없음: %s", want, got)
		}
	}
	// 매핑에 없는 포트는 기본 라벨만
	got = e.labels("modbus-slave-9999")
	if strings.Contains(got, "collector=") {
		t.Errorf("매핑 없는 포트에 collector 라벨: %s", got)
	}
}

func TestExporterLabelsWithoutMapping(t *testing.T) {
	e := newExporter(nil)
	got := e.labels("modbus-slave-502")
	if strings.Contains(got, "collector=") {
		t.Errorf("매핑 없는데 collector 라벨: %s", got)
	}
	if !strings.Contains(got, `port="502"`) {
		t.Errorf("port 라벨 누락: %s", got)
	}
}
