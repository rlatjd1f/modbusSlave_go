package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// exporter 는 에이전트가 이미 측정하고 있는 값을 Prometheus 형식으로 내보낸다.
//
// 원래는 cAdvisor 로 받을 계획이었으나, cAdvisor 는 호스트 cgroup 접근에 의존해
// 환경에 따라 컨테이너를 전혀 인식하지 못한다(실제로 Docker Desktop 과 Ubuntu
// 양쪽에서 name 라벨이 비어 나왔다). 네트워크 트래픽이 이 시스템의 핵심 측정
// 대상이므로 Docker API 를 직접 읽는 이 경로 하나로 통일했다.
type exporter struct {
	mu      sync.RWMutex
	samples map[string]sample
}

func newExporter() *exporter {
	return &exporter{samples: map[string]sample{}}
}

// update 는 한 주기 분의 측정값을 갈아 끼운다.
func (e *exporter) update(samples map[string]sample) {
	e.mu.Lock()
	e.samples = samples
	e.mu.Unlock()
}

type metricDef struct {
	name string
	help string
	typ  string
	val  func(sample) float64
}

var metricDefs = []metricDef{
	{"modbus_slave_transmit_bytes_total", "슬레이브 컨테이너 송신 누적 바이트", "counter",
		func(s sample) float64 { return float64(s.TxTotal) }},
	{"modbus_slave_transmit_mbps", "슬레이브 컨테이너 아웃바운드 전송률(Mbps)", "gauge",
		func(s sample) float64 { return s.TxMbps }},
	{"modbus_slave_receive_mbps", "슬레이브 컨테이너 인바운드 전송률(Mbps)", "gauge",
		func(s sample) float64 { return s.RxMbps }},
	{"modbus_slave_cpu_vcpu", "슬레이브 컨테이너가 쓰는 vCPU 수", "gauge",
		func(s sample) float64 { return s.VCPU }},
	{"modbus_slave_memory_bytes", "슬레이브 컨테이너 working set 메모리", "gauge",
		func(s sample) float64 { return float64(s.Mem) }},
}

func (e *exporter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	e.mu.RLock()
	samples := e.samples
	e.mu.RUnlock()

	names := make([]string, 0, len(samples))
	for n := range samples {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, m := range metricDefs {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, m.typ)
		var total float64
		for _, n := range names {
			v := m.val(samples[n])
			fmt.Fprintf(&b, "%s{port=%q,container=%q} %g\n", m.name, fieldName(n), n, v)
			total += v
		}
		// counter 는 합계를 따로 내지 않는다. Prometheus 에서 sum() 으로 구하면 된다.
		if m.typ == "gauge" {
			fmt.Fprintf(&b, "# HELP %s_total 전체 슬레이브 합계\n# TYPE %s_total gauge\n", m.name, m.name)
			fmt.Fprintf(&b, "%s_total %g\n", m.name, total)
		}
	}

	b.WriteString("# HELP modbus_slave_containers 측정 대상 컨테이너 수\n")
	b.WriteString("# TYPE modbus_slave_containers gauge\n")
	fmt.Fprintf(&b, "modbus_slave_containers %d\n", len(names))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// serveMetrics 는 /metrics 를 여는 HTTP 서버를 띄운다.
// 실패해도 Redis 전송은 계속되어야 하므로 로그만 남긴다.
func serveMetrics(addr string, e *exporter, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", e)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	go func() {
		log.Info("메트릭 노출 시작", "addr", addr, "path", "/metrics")
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Error("메트릭 서버 종료", "err", err)
		}
	}()
}
