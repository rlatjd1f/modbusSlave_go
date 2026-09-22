// metrics-agent 는 modbus 슬레이브 컨테이너들의 아웃바운드 네트워크 전송률을
// 주기적으로 측정해 Redis 해시에 기록한다.
//
// 키 하나에 필드로 담는다.
//
//	HSET <key> 502 0.86  503 0.87 ... total 41.20 ts 1758500000
//	EXPIRE <key> <주기 x 3>
//
// TTL 을 거는 이유는 에이전트가 죽었을 때 소비하는 쪽이 낡은 값을
// 실시간 값으로 오인하지 않게 하기 위함이다.
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
	"sort"
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
	redisAddr string
	redisPass string
	redisDB   int
	key       string
	interval  time.Duration
	prefix    string
	socket    string
	timeout   time.Duration
	logLevel  string
	once      bool
	metrics   string
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

	exp := newExporter()
	if cfg.metrics != "" {
		serveMetrics(cfg.metrics, exp, log)
	}

	s := &sampler{docker: docker, prefix: cfg.prefix, log: log}

	// 첫 표본은 기준점만 잡는다. 차이를 낼 이전 값이 없으므로 전송하지 않는다.
	if err := s.sample(ctx); err != nil {
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

		rates, err := s.rates(ctx)
		if err != nil {
			log.Error("표본 수집 실패", "err", err)
			continue
		}
		if len(rates) == 0 {
			log.Debug("대상 컨테이너 없음", "prefix", cfg.prefix)
			continue
		}

		exp.update(rates, s.prev)

		fields := buildFields(rates)
		if err := rdb.Publish(cfg.key, fields, cfg.interval*ttlFactor); err != nil {
			fails++
			// 연결이 끊겨도 슬레이브에는 영향이 없다. 로그만 남기고 다음 주기에 재시도한다.
			log.Error("Redis 전송 실패", "err", err, "연속실패", fails)
			continue
		}
		if fails > 0 {
			log.Info("Redis 전송 복구", "직전연속실패", fails)
			fails = 0
		}
		log.Debug("전송 완료", "컨테이너", len(rates), "total_mbps", totalOf(rates))

		if cfg.once {
			return 0
		}
	}
}

// sampler 는 직전 표본을 들고 있다가 차이로 전송률을 낸다.
type sampler struct {
	docker *dockerapi.Client
	prefix string
	log    *slog.Logger

	prev map[string]uint64 // 컨테이너 이름 -> 송신 누적 바이트
	at   time.Time
}

// sample 은 현재 누적값을 읽어 저장한다.
func (s *sampler) sample(ctx context.Context) error {
	cur, err := s.collect(ctx)
	if err != nil {
		return err
	}
	s.prev, s.at = cur, time.Now()
	return nil
}

func (s *sampler) collect(ctx context.Context) (map[string]uint64, error) {
	list, err := s.docker.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(list))
	for _, c := range list {
		name := c.Name()
		if !strings.HasPrefix(name, s.prefix) {
			continue
		}
		_, tx, err := s.docker.NetBytes(ctx, c.ID)
		if err != nil {
			// 방금 사라진 컨테이너일 수 있다. 하나 때문에 주기 전체를 버리지 않는다.
			s.log.Debug("네트워크 통계 조회 실패", "container", name, "err", err)
			continue
		}
		out[name] = tx
	}
	return out, nil
}

// rates 는 직전 표본과의 차이로 컨테이너별 Mbps 를 낸다.
func (s *sampler) rates(ctx context.Context) (map[string]float64, error) {
	cur, err := s.collect(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	elapsed := now.Sub(s.at).Seconds()
	rates := make(map[string]float64, len(cur))
	if s.prev != nil && elapsed > 0 {
		for name, tx := range cur {
			before, ok := s.prev[name]
			if !ok || tx < before {
				// 새로 뜬 컨테이너이거나 재시작으로 카운터가 되감겼다.
				rates[name] = 0
				continue
			}
			rates[name] = float64(tx-before) * 8 / 1e6 / elapsed
		}
	} else {
		for name := range cur {
			rates[name] = 0
		}
	}
	s.prev, s.at = cur, now
	return rates, nil
}

// buildFields 는 Redis 해시에 쓸 이름/값 쌍을 만든다.
// 필드 이름은 컨테이너 이름에서 접두사를 뗀 값(= 호스트 포트)이다.
func buildFields(rates map[string]float64) []string {
	names := make([]string, 0, len(rates))
	for n := range rates {
		names = append(names, n)
	}
	sort.Strings(names)

	fields := make([]string, 0, len(names)*2+4)
	var total float64
	for _, n := range names {
		fields = append(fields, fieldName(n), strconv.FormatFloat(rates[n], 'f', 2, 64))
		total += rates[n]
	}
	fields = append(fields,
		"total", strconv.FormatFloat(total, 'f', 2, 64),
		"ts", strconv.FormatInt(time.Now().Unix(), 10))
	return fields
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

func totalOf(rates map[string]float64) float64 {
	var t float64
	for _, v := range rates {
		t += v
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
