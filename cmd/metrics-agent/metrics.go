package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// exporter 는 에이전트가 이미 측정하고 있는 값을 Prometheus 형식으로도 내보낸다.
//
// cAdvisor 로도 같은 것을 볼 수 있지만, cAdvisor 는 호스트 cgroup 접근에 의존해
// 환경에 따라 컨테이너를 인식하지 못한다. 네트워크 트래픽이 이 시스템의 핵심
// 측정 대상이므로, Docker API 를 직접 읽는 이 경로를 별도 소스로 둔다.
type exporter struct {
	mu    sync.RWMutex
	rates map[string]float64 // 컨테이너 이름 -> Mbps
	bytes map[string]uint64  // 컨테이너 이름 -> 송신 누적 바이트
}

func newExporter() *exporter {
	return &exporter{rates: map[string]float64{}, bytes: map[string]uint64{}}
}

// update 는 한 주기 분의 측정값을 갈아 끼운다.
func (e *exporter) update(rates map[string]float64, bytes map[string]uint64) {
	e.mu.Lock()
	e.rates, e.bytes = rates, bytes
	e.mu.Unlock()
}

func (e *exporter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	e.mu.RLock()
	rates, bytes := e.rates, e.bytes
	e.mu.RUnlock()

	names := make([]string, 0, len(rates))
	for n := range rates {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("# HELP modbus_slave_transmit_bytes_total 슬레이브 컨테이너 송신 누적 바이트\n")
	b.WriteString("# TYPE modbus_slave_transmit_bytes_total counter\n")
	for _, n := range names {
		fmt.Fprintf(&b, "modbus_slave_transmit_bytes_total{port=%q,container=%q} %d\n",
			fieldName(n), n, bytes[n])
	}

	b.WriteString("# HELP modbus_slave_transmit_mbps 슬레이브 컨테이너 아웃바운드 전송률(Mbps)\n")
	b.WriteString("# TYPE modbus_slave_transmit_mbps gauge\n")
	var total float64
	for _, n := range names {
		fmt.Fprintf(&b, "modbus_slave_transmit_mbps{port=%q,container=%q} %.4f\n",
			fieldName(n), n, rates[n])
		total += rates[n]
	}

	b.WriteString("# HELP modbus_slave_transmit_mbps_total 전체 슬레이브 통합 아웃바운드(Mbps)\n")
	b.WriteString("# TYPE modbus_slave_transmit_mbps_total gauge\n")
	fmt.Fprintf(&b, "modbus_slave_transmit_mbps_total %.4f\n", total)

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
