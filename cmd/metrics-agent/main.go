// metrics-agent 는 modbus 슬레이브 컨테이너들의 아웃바운드 네트워크 전송률을
// 주기적으로 측정해 Redis 에 기록한다.
//
// 전체 통합 아웃바운드를 Mbps 로 키 하나에 담는다.
//
//	SET <key> 41.203 EX <주기 x 3>
//
// TTL 을 거는 이유는 에이전트가 죽었을 때 소비하는 쪽이 낡은 값을
// 실시간 값으로 오인하지 않게 하기 위함이다.
// 슬레이브별 값은 Prometheus 로 노출되어 Grafana 에서 본다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"modbus-slave/internal/dockerapi"
	"modbus-slave/internal/redisc"
)

const (
	defaultKey      = "liz.stats.server.modbus.network.traffic"
	defaultInterval = 3 * time.Second
	defaultPrefix   = "modbus-slave-"
	// TTL 은 주기의 몇 배로 잡을지. 한두 번 걸러도 값이 살아있게 3배로 둔다.
	ttlFactor = 3
)

type config struct {
	redisAddr  string
	redisPass  string
	redisDB    int
	key        string
	interval   time.Duration
	prefix     string
	socket     string
	timeout    time.Duration
	logLevel   string
	once       bool
	metrics    string
	collectors string
}

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := parseFlags(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "설정 오류: %v\n", err)
		return 2
	}

	log := newLogger(cfg.logLevel)
	docker := dockerapi.New(cfg.socket, cfg.timeout)
	rdb := redisc.New(cfg.redisAddr, cfg.redisPass, cfg.redisDB, cfg.timeout)
	defer rdb.Close()

	log.Info("metrics-agent 시작",
		"redis", cfg.redisAddr, "db", cfg.redisDB, "key", cfg.key,
		"interval", cfg.interval.String(), "prefix", cfg.prefix)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmap, err := loadCollectors(cfg.collectors)
	if err != nil {
		// 매핑이 잘못됐으면 라벨이 조용히 틀리는 것보다 기동 실패가 낫다.
		fmt.Fprintf(os.Stderr, "콜렉터 매핑 오류: %v\n", err)
		return 2
	}
	if len(cmap) > 0 {
		log.Info("콜렉터 매핑 적용", "file", cfg.collectors, "포트수", len(cmap))
	} else {
		log.Debug("콜렉터 매핑 없음", "file", cfg.collectors)
	}

	exp := newExporter(cmap)
	if cfg.metrics != "" {
		serveMetrics(cfg.metrics, exp, log)
	}

	s := &sampler{docker: docker, prefix: cfg.prefix, log: log}

	// 첫 표본은 기준점만 잡는다. 차이를 낼 이전 값이 없으므로 전송하지 않는다.
	if err := s.prime(ctx); err != nil {
		log.Error("첫 표본 수집 실패", "err", err)
	}

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	var fails int
	for {
		select {
		case <-ctx.Done():
			log.Info("종료")
			return 0
		case <-ticker.C:
		}

		samples, err := s.measure(ctx)
		if err != nil {
			log.Error("표본 수집 실패", "err", err)
			continue
		}
		if len(samples) == 0 {
			log.Debug("대상 컨테이너 없음", "prefix", cfg.prefix)
			continue
		}

		exp.update(samples)

		// Redis 에는 통합 아웃바운드만 싣는다.
		if err := rdb.Set(cfg.key, buildValue(samples), cfg.interval*ttlFactor); err != nil {
			fails++
			// 연결이 끊겨도 슬레이브에는 영향이 없다. 로그만 남기고 다음 주기에 재시도한다.
			log.Error("Redis 전송 실패", "err", err, "연속실패", fails)
			continue
		}
		if fails > 0 {
			log.Info("Redis 전송 복구", "직전연속실패", fails)
			fails = 0
		}
		log.Debug("전송 완료", "컨테이너", len(samples), "total_mbps", totalOf(samples))

		if cfg.once {
			return 0
		}
	}
}

// sample 은 한 주기의 계산 결과다.
type sample struct {
	TxMbps  float64 // 아웃바운드 전송률
	RxMbps  float64 // 인바운드 전송률
	VCPU    float64 // 사용 중인 vCPU 수
	Mem     uint64  // working set 바이트
	TxTotal uint64  // 송신 누적 (Prometheus counter 용)
}

// sampler 는 직전 표본을 들고 있다가 차이로 전송률과 CPU 사용량을 낸다.
type sampler struct {
	docker *dockerapi.Client
	prefix string
	log    *slog.Logger

	prev map[string]dockerapi.Stats
	at   time.Time
}

// prime 은 첫 기준점을 잡는다. 차이를 낼 이전 값이 없으므로 결과를 내지 않는다.
func (s *sampler) prime(ctx context.Context) error {
	cur, err := s.collect(ctx)
	if err != nil {
		return err
	}
	s.prev, s.at = cur, time.Now()
	return nil
}

func (s *sampler) collect(ctx context.Context) (map[string]dockerapi.Stats, error) {
	list, err := s.docker.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]dockerapi.Stats, len(list))
	for _, c := range list {
		name := c.Name()
		if !strings.HasPrefix(name, s.prefix) {
			continue
		}
		st, err := s.docker.Stats(ctx, c.ID)
		if err != nil {
			// 방금 사라진 컨테이너일 수 있다. 하나 때문에 주기 전체를 버리지 않는다.
			s.log.Debug("통계 조회 실패", "container", name, "err", err)
			continue
		}
		out[name] = st
	}
	return out, nil
}

// measure 는 직전 표본과의 차이로 컨테이너별 지표를 낸다.
func (s *sampler) measure(ctx context.Context) (map[string]sample, error) {
	cur, err := s.collect(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	elapsed := now.Sub(s.at).Seconds()
	out := make(map[string]sample, len(cur))

	for name, st := range cur {
		m := sample{Mem: st.MemBytes, TxTotal: st.TxBytes}
		before, ok := s.prev[name]
		// 새로 뜬 컨테이너이거나 재시작으로 카운터가 되감기면 이번 주기는 0으로 둔다.
		if ok && elapsed > 0 {
			if st.TxBytes >= before.TxBytes {
				m.TxMbps = float64(st.TxBytes-before.TxBytes) * 8 / 1e6 / elapsed
			}
			if st.RxBytes >= before.RxBytes {
				m.RxMbps = float64(st.RxBytes-before.RxBytes) * 8 / 1e6 / elapsed
			}
			if st.CPUNanos >= before.CPUNanos {
				// CPU 누적은 나노초다. 경과 시간으로 나누면 사용 중인 vCPU 수가 된다.
				m.VCPU = float64(st.CPUNanos-before.CPUNanos) / 1e9 / elapsed
			}
		}
		out[name] = m
	}
	s.prev, s.at = cur, now
	return out, nil
}

// redisDecimals 는 Redis 에 쓰는 Mbps 값의 소수 자릿수다.
const redisDecimals = 3

// buildValue 는 Redis 에 쓸 값을 만든다.
// 전체 통합 아웃바운드 Mbps 하나뿐이다.
func buildValue(samples map[string]sample) string {
	return strconv.FormatFloat(totalOf(samples), 'f', redisDecimals, 64)
}

// fieldName 은 "modbus-slave-502" 에서 "502" 를 뽑는다.
// 접두사가 없으면 이름을 그대로 쓴다.
func fieldName(container string) string {
	if i := strings.LastIndex(container, "-"); i >= 0 && i+1 < len(container) {
		if _, err := strconv.Atoi(container[i+1:]); err == nil {
			return container[i+1:]
		}
	}
	return container
}

func totalOf(samples map[string]sample) float64 {
	var t float64
	for _, v := range samples {
		t += v.TxMbps
	}
	return t
}

func parseFlags(args []string, getenv func(string) string, out io.Writer) (*config, error) {
	envStr := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}

	fs := flag.NewFlagSet("metrics-agent", flag.ContinueOnError)
	fs.SetOutput(out)
	cfg := &config{}
	var dbStr string

	fs.StringVar(&cfg.redisAddr, "redis-addr", envStr("REDIS_ADDR", ""), "Redis 주소 host:port (필수)")
	fs.StringVar(&cfg.redisPass, "redis-password", envStr("REDIS_PASSWORD", ""), "Redis 비밀번호 (없으면 생략)")
	fs.StringVar(&dbStr, "redis-db", envStr("REDIS_DB", "0"), "Redis DB 번호")
	fs.StringVar(&cfg.key, "key", envStr("REDIS_KEY", defaultKey), "값을 쓸 해시 키")
	fs.DurationVar(&cfg.interval, "interval", defaultInterval, "측정 주기")
	fs.StringVar(&cfg.prefix, "prefix", envStr("NAME_PREFIX", defaultPrefix), "대상 컨테이너 이름 접두사")
	fs.StringVar(&cfg.socket, "docker-socket", envStr("DOCKER_SOCKET", "/var/run/docker.sock"), "Docker 소켓 경로")
	fs.DurationVar(&cfg.timeout, "timeout", 5*time.Second, "Docker/Redis 요청 타임아웃")
	fs.StringVar(&cfg.logLevel, "log-level", envStr("LOG_LEVEL", "info"), "debug|info|warn|error")
	fs.StringVar(&cfg.metrics, "metrics-addr", envStr("METRICS_ADDR", ":9101"), "Prometheus 메트릭 노출 주소 (빈 값이면 끔)")
	fs.StringVar(&cfg.collectors, "collectors", envStr("COLLECTORS_FILE", "/etc/modbus/collectors.conf"), "포트-콜렉터 매핑 파일 (없으면 라벨 생략)")
	fs.BoolVar(&cfg.once, "once", false, "한 번만 전송하고 종료 (점검용)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if cfg.redisAddr == "" {
		return nil, errors.New("--redis-addr 는 필수입니다")
	}
	if !strings.Contains(cfg.redisAddr, ":") {
		cfg.redisAddr += ":6379"
	}
	db, err := strconv.Atoi(dbStr)
	if err != nil || db < 0 {
		return nil, fmt.Errorf("--redis-db 는 0 이상의 정수여야 함: %q", dbStr)
	}
	cfg.redisDB = db
	if cfg.interval < time.Second {
		return nil, fmt.Errorf("--interval 은 1초 이상이어야 함: %s", cfg.interval)
	}
	return cfg, nil
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
