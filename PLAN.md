# Modbus Slave (Docker) 개발 계획서

작성일: 2026-09-15

---

## 1. 목표

컨테이너 1개 = Modbus TCP Slave(Server) 1개로 동작하는 경량 시뮬레이터를 만든다.
실행 파라미터로 **리스닝 포트**와 **레지스터 개수**를 지정하며, 모든 레지스터는
`uint16` 규격이고 읽기 요청에 대해 항상 **0**을 반환한다.

### 요구사항 정리

| 항목 | 내용 |
|---|---|
| 배포 단위 | Docker 컨테이너 1개 = Slave 1개 (멀티 Unit ID 미지원) |
| 프로토콜 | Modbus TCP (MBAP 헤더 + PDU) |
| 필수 파라미터 | 포트 (`--port`) |
| 선택 파라미터 | 레지스터 개수 (`--registers`, 기본값 **1000**) |
| 레지스터 규격 | unsigned 16-bit (2 Byte), Big-Endian |
| 반환 값 | 전 주소 0x0000 고정 |
| Unit ID | `--unit-id`, 기본값 **1** |
| 함수 코드 | `--function-code`, 기본값 **0x03** |

---

## 2. 언어 선정: **Go**

| 후보 | 장점 | 단점 | 판단 |
|---|---|---|---|
| **Go** | 정적 단일 바이너리(외부 런타임 0), goroutine 기반 동시 접속 처리, 표준 `net` 패키지만으로 충분, `scratch` 이미지에 넣으면 **약 3~6MB**, 빌드/테스트 도구 내장 | GC 존재 (단 이 워크로드에선 무의미한 수준) | **채택** |
| Rust | 최고 성능, GC 없음 | 개발·빌드 시간 증가, 이 정도 로직에 과함 | 보류 |
| C | 최소 바이너리 | 메모리 안전성/동시성 직접 관리 부담 | 제외 |
| Python | 빠른 개발 | 런타임 포함 이미지 50MB+, 동시성/처리량 열위 | 제외 |

> 근거: 요청 처리 로직 자체는 매우 단순(응답 값이 상수)하므로 병목은 전적으로
> **네트워크 I/O와 동시 커넥션 처리**다. Go의 goroutine + epoll/kqueue 모델이
> 구현 난이도 대비 가장 효율이 좋다. 로컬에 Go 1.26 설치 확인 완료.

**외부 Modbus 라이브러리는 사용하지 않는다.** 필요한 함수 코드가 소수이고
프레임 구조가 단순해 직접 구현하는 편이 의존성·이미지 크기·디버깅 면에서 유리하다.

---

## 3. 프로토콜 설계

### 3.1 MBAP 헤더 (7 byte)

```
+---------------+---------------+---------------+-----------+
| Transaction ID| Protocol ID   | Length        | Unit ID   |
| 2 byte        | 2 byte (=0)   | 2 byte        | 1 byte    |
+---------------+---------------+---------------+-----------+
```

- `Length` = Unit ID(1) + PDU 길이
- 모든 다중 바이트 필드는 Big-Endian

### 3.2 함수 코드

응답할 함수 코드는 `--function-code`로 지정하며, **지정하지 않으면 0x03만 활성화**된다.
활성 목록에 없는 함수 코드는 전부 Exception 0x01 (Illegal Function)으로 응답한다.

| FC | 이름 | 기본 | 구현 차수 | 동작 |
|---|---|---|---|---|
| **0x03** | Read Holding Registers | **✅ 기본 활성** | 1차 | quantity × 0x0000 반환 |
| 0x04 | Read Input Registers | 비활성 | 1차 | quantity × 0x0000 반환 |
| 0x01 | Read Coils | 비활성 | 2차 | 전 비트 0 반환 |
| 0x02 | Read Discrete Inputs | 비활성 | 2차 | 전 비트 0 반환 |
| 0x06 | Write Single Register | 비활성 | 2차 | 검증 후 echo 응답, 값은 버림 |
| 0x10 | Write Multiple Registers | 비활성 | 2차 | 검증 후 정상 응답, 값은 버림 |
| 그 외 | - | - | - | Exception 0x01 (Illegal Function) |

지정 형식 — 10진수/16진수 모두 허용하고, 쉼표로 복수 지정한다.

```
--function-code 3            # 기본값과 동일
--function-code 4            # FC04만 응답, FC03은 0x01
--function-code 3,4          # 둘 다 응답
--function-code 0x03,0x04,0x06
```

> 쓰기(0x06/0x10)를 활성화한 경우 **수락 후 폐기(accept-and-discard)** 로 처리한다.
> "값은 항상 0" 요구사항을 유지하면서, 쓰기를 시도하는 마스터가 에러로
> 중단되지 않게 하기 위함이다.

### 3.3 예외 응답 규칙

| 조건 | Exception Code |
|---|---|
| 미지원 함수 코드 | 0x01 Illegal Function |
| `quantity`가 1~125 범위 밖 (FC03/04) | 0x03 Illegal Data Value |
| `start + quantity > --registers` 또는 `start > 0xFFFF` | 0x02 Illegal Data Address |

응답 시 함수 코드에 0x80을 OR 하여 전송한다.

### 3.4 프레임 검증

- `Protocol ID != 0` → 커넥션 드롭 (Modbus 프레임 아님)
- `Length`가 2 미만이거나 254 초과 → 커넥션 드롭 (PDU 최대 253 + Unit ID 1)
- Unit ID: 기본값 **1**. 요청의 Unit ID가 설정값과 다르면 **응답하지 않고 무시**한다
  (Modbus TCP 스펙 권고 동작 — 게이트웨이 뒤 다른 노드 앞으로 온 프레임으로 간주).
  `--unit-id 0` 지정 시에만 모든 Unit ID를 수락하는 예외 모드로 동작한다.

---

## 4. 파라미터 설계

| 플래그 | 환경변수 | 기본값 | 설명 |
|---|---|---|---|
| `--port` | `MODBUS_PORT` | (없음, **필수**) | 리스닝 TCP 포트. 1~65535 |
| `--registers` | `MODBUS_REGISTERS` | `1000` | 레지스터 개수. 1~65536 |
| `--unit-id` | `MODBUS_UNIT_ID` | `1` | 이 Unit ID에만 응답. `0` = 전체 수락 |
| `--function-code` | `MODBUS_FUNCTION_CODE` | `3` (0x03) | 활성화할 함수 코드. 쉼표로 복수 지정 |
| `--bind` | `MODBUS_BIND` | `0.0.0.0` | 바인드 주소 |
| `--max-conns` | `MODBUS_MAX_CONNS` | `256` | 동시 접속 상한 |
| `--idle-timeout` | `MODBUS_IDLE_TIMEOUT` | `0` (무제한) | 유휴 커넥션 종료 시간 |
| `--log-level` | `MODBUS_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |

- 우선순위: **CLI 플래그 > 환경변수 > 기본값**
- `--port` 미지정 시 usage 출력 후 **exit code 2**로 즉시 종료
- 값 검증 실패 시에도 동일하게 조기 종료 — 잘못된 설정의 컨테이너가 조용히
  살아있는 상황을 만들지 않는다. 검증 항목:
  - 포트 1~65535, 레지스터 1~65536, Unit ID 0~255
  - `--function-code`: 파싱 가능한 값인지, **구현된 FC 목록에 있는지**.
    미구현 FC(예: `--function-code 0x2B`)를 주면 기동 실패시켜 오설정을 즉시 드러낸다.

---

## 5. 아키텍처

```
main.go            플래그/환경변수 파싱, 검증, 시그널 핸들링, graceful shutdown
config/config.go   Config 구조체 + Load() + Validate()
server/server.go   Listener, 커넥션 accept 루프, 동시 접속 제한
server/conn.go     커넥션별 goroutine: MBAP 읽기 → 핸들러 → 응답 쓰기
modbus/frame.go    MBAP 인코딩/디코딩
modbus/pdu.go      PDU 파싱, 함수 코드 디스패치, 예외 생성
modbus/handler.go  FC별 응답 생성 (제로 값)
healthcheck.go     -healthcheck 모드 (컨테이너 HEALTHCHECK용)
```

### 핵심 설계 포인트

1. **응답 버퍼 사전 할당(zero-alloc 경로)**
   응답 데이터가 전부 0이므로, 최대 크기(125 레지스터 = 250 byte)의 제로 슬라이스를
   패키지 레벨 상수로 하나 두고 잘라 쓴다. 요청마다 페이로드를 새로 만들지 않는다.

2. **레지스터 배열을 실제로 할당하지 않는다**
   `--registers`는 **주소 범위 검증용 숫자**로만 사용한다. 값이 항상 0이므로
   65536개를 지정해도 메모리 사용량은 변하지 않는다.

3. **커넥션당 goroutine + `bufio.Reader`/`Writer`**
   `io.ReadFull`로 헤더 7바이트 → `Length`만큼 본문을 읽는 단순 루프.
   TCP 세그먼트 경계와 무관하게 정확히 동작한다.

4. **함수 코드 디스패치는 256칸 배열 룩업**
   활성 FC 집합을 `[256]handlerFunc`로 펼쳐 두고 `table[fc]`로 바로 분기한다.
   맵 조회나 조건 분기 체인 없이 상수 시간에 처리되고, 비활성 FC는 자연스럽게
   Illegal Function 핸들러를 가리킨다.

5. **`TCP_NODELAY` 활성화** (Go 기본값) — 소형 응답 지연 방지.

6. **Graceful shutdown**: SIGTERM/SIGINT 수신 시 리스너를 닫고 진행 중인
   커넥션을 최대 5초 대기 후 종료. `docker stop` 시 10초 대기 없이 즉시 내려간다.

---

## 6. Docker 설계

### 6.1 Dockerfile (멀티스테이지)

```dockerfile
# --- build ---
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/modbus-slave ./cmd/modbus-slave

# --- runtime ---
FROM scratch
COPY --from=build /out/modbus-slave /modbus-slave
USER 65534:65534
ENTRYPOINT ["/modbus-slave"]
```

- `scratch` 기반 → 최종 이미지 **약 3~6MB**, 취약점 표면 최소화
- non-root(65534, nobody)로 실행. 1024 미만 포트는 컨테이너 내부에서 쓰지 않고
  `-p 502:5020` 형태로 호스트 매핑을 권장
- 셸이 없으므로 HEALTHCHECK는 **바이너리 자체의 `-healthcheck` 모드**로 구현
  (자기 포트로 **설정된 Unit ID + 활성 FC 중 첫 번째**를 써서 레지스터 1개를
  요청하고 정상 응답을 확인한다. 설정과 무관하게 항상 FC03을 쓰면
  `--function-code 4` 인스턴스가 영구 unhealthy가 되므로 반드시 설정을 따른다)

```dockerfile
HEALTHCHECK --interval=30s --timeout=3s --start-period=2s --retries=3 \
  CMD ["/modbus-slave", "-healthcheck"]
```

### 6.2 실행 예시

```bash
# 기본 (레지스터 1000개)
docker run -d -p 5020:5020 modbus-slave --port 5020

# 레지스터 5000개
docker run -d -p 5021:5021 modbus-slave --port 5021 --registers 5000

# Unit ID 2, Input Register(FC04)로 응답
docker run -d -p 5022:5022 modbus-slave --port 5022 --unit-id 2 --function-code 4

# FC03 + FC04 동시 활성화
docker run -d -p 5023:5023 modbus-slave --port 5023 --function-code 3,4

# 환경변수 방식
docker run -d -p 5024:5024 \
  -e MODBUS_PORT=5024 -e MODBUS_REGISTERS=200 -e MODBUS_UNIT_ID=1 modbus-slave
```

### 6.3 다중 Slave (docker-compose)

```yaml
services:
  slave-1:
    image: modbus-slave
    command: ["--port", "5020", "--registers", "1000"]
    ports: ["5020:5020"]
  slave-2:
    image: modbus-slave
    command: ["--port", "5020", "--registers", "5000"]
    ports: ["5021:5020"]
```

> 컨테이너마다 내부 포트를 동일하게 두고 호스트 포트만 다르게 매핑하는 패턴,
> 또는 내부 포트까지 각각 다르게 주는 패턴 모두 지원한다.

---

## 7. 테스트 계획

### 7.1 단위 테스트 (Go test)

- MBAP 인코딩/디코딩 왕복 테스트
- FC03/04 정상 응답 바이트 검증 (byte count = quantity × 2, 전부 0)
- 경계값: `start=0`, `start+qty == registers` (정상) / `== registers+1` (0x02)
- `quantity = 0`, `126` → 0x03
- 비활성 FC → 0x01 (기본 설정에서 FC04 요청 시 0x01, `--function-code 4` 설정에서 FC03 요청 시 0x01)
- Unit ID 불일치 요청 → **응답 없음**(타임아웃), 커넥션은 유지
- `--unit-id 0` 설정 시 임의 Unit ID 요청에 전부 응답
- `--function-code` 파싱: `3` / `0x03` / `3,4` / 공백 포함 / 중복 지정 / 미구현 FC(기동 실패)
- `Protocol ID != 0` → 커넥션 종료
- **분할 수신 테스트**: 프레임을 1바이트씩 나눠 보내도 정상 파싱되는지

### 7.2 통합 테스트

- `net.Pipe` 또는 실제 로컬 리스너 대상 end-to-end 테스트
- 외부 클라이언트 교차 검증:
  - `mbpoll -m tcp -a 1 -r 1 -c 10 -p 5020 127.0.0.1` (기본 설정: Unit ID 1 + FC03)
  - `mbpoll -m tcp -a 1 -t 3 ...` (FC04 인스턴스 대상)
  - `pymodbus` 클라이언트 스크립트로 FC03/04/예외 응답 확인
- 동시 커넥션 50개 × 각 1000요청 → 응답 정확성 및 누락 없음 확인

### 7.3 성능 목표 및 실측 (2026-09-15)

| 항목 | 목표 | 실측 | 판정 |
|---|---|---|---|
| 동시 커넥션 | 256 이상 안정 동작 | 컨테이너당 100, 전체 2,000 정상 | ✅ |
| 단일 커넥션 처리량 | 20k req/s 이상 (로컬 루프백) | 네이티브 루프백 60.4k / Docker 포트 퍼블리싱 경유 10.7k | ⚠️ 경로 의존 |
| 컨테이너 RSS | 20MB 미만 | 실제 워크로드 5.0MiB / 최대 부하 9.24MiB | ✅ |
| 스캔 주기 준수 | 1초 예산 내 | 2,000 커넥션에서도 주기 초과 0 | ✅ |
| 애플리케이션 로직 비용 | - | 11.3 ns/op, 0 allocs/op | ✅ |

측정 환경: Apple M-series(10코어), Go 1.26, Docker Desktop(linux/arm64).

#### 실제 워크로드 재현 — 이것이 사이징의 기준이다

대상 워크로드는 **컨테이너당 수십 개의 커넥션, 각 클라이언트가 1초마다 1000개
레지스터를 전부 훑는** 패턴이다. 현재 테스트 기준은 컨테이너당 50 커넥션이다.

**FC03 은 한 요청에 최대 125개만 읽을 수 있다(스펙상 0x0001~0x007D).**
따라서 1000 레지스터 스캔 = **8요청**이고, 커넥션 50개는 초당 50요청이 아니라
**초당 400요청**이다. 이 8배가 사이징 전체를 좌우한다.
(클라이언트가 `qty=1000` 을 한 번에 보내면 예외 0x03 을 받는다.)

컨테이너 20개를 각각 `--cpus 2`(GOMAXPROCS=2 고정)로 띄우고
[scripts/poll.go](scripts/poll.go) 로 측정했다.

| 컨테이너당 커넥션 | 총 커넥션 | 총 req/s | CPU | 메모리(20개 합) | 컨테이너당 | 스캔 p99 |
|---|---|---|---|---|---|---|
| 25 | 500 | 3,530 | 0.102 vCPU | 103 MiB | 5.14 MiB | 84 ms |
| **50 (현재 기준)** | **1,000** | **7,305** | **0.168 vCPU** | **101 MiB** | **5.02 MiB** | **141 ms** |
| 100 | 2,000 | 14,118 | 0.417 vCPU | 116 MiB | 5.78 MiB | 318 ms |

- 전 구간 **에러 0, 주기 초과 0** — 2,000 커넥션에서도 모든 스캔이 1초 예산 안에 끝났다.
- **메모리는 커넥션 수와 거의 무관하게 컨테이너당 5~6 MiB** 다. 페이싱이 걸린 폴링은
  최대 처리량 부하(9.24MiB)와 달리 버퍼가 커지지 않는다.
- CPU 는 커넥션 수에 거의 선형이다. 이 구간의 처리량은 **vCPU 당 34~43k req/s**.
- 스캔 지연(p50 124ms / p99 141ms @ 50커넥션)은 **서버가 아니라 측정 경로의 값**이다.
  Docker Desktop 포트 포워딩을 거치고, 1000개 클라이언트가 같은 1초 tick 에 몰린다.
  EC2 실측으로 대체해야 하는 수치다.

#### 처리량

| 조건 | 결과 |
|---|---|
| 네이티브 루프백, 1 커넥션, qty=10 | 60,448 req/s |
| 네이티브 루프백, 50 커넥션, qty=10 | 163,872 req/s |
| 네이티브 루프백, 256 커넥션, qty=125 | 184,505 req/s |
| 컨테이너(`--cpus 2`) + 포트 퍼블리싱, 256 커넥션, qty=125 | 71,956 req/s (컨테이너 CPU 56.3%) |
| 컨테이너(`--cpus 2`) + 포트 퍼블리싱, 1 커넥션, qty=10 | 10,691 req/s |

- 컨테이너 측정 기준 **요청당 CPU 약 15.6 µs → vCPU 당 약 64k req/s**.
  사이징 계산에는 보수적으로 **25 µs / vCPU 당 40k req/s** 를 쓴다.
- CPU 시간의 대부분이 sys 이다. 병목은 연산이 아니라 커널 네트워크 스택이며,
  §5의 "레지스터 배열 미할당 / 제로 슬라이스 재사용" 설계대로 로직 비용은 사실상 0이다.
- **단일 커넥션 처리량은 왕복 지연에 묶인다.** 네이티브 60.4k 가 Docker 포트
  퍼블리싱을 거치면 10.7k 로 떨어진다. 목표 20k req/s 는 경로를 명시해야 의미가 있다.
- 위 "단일 커넥션 20k req/s" 는 **인스턴스 하나의 능력치** 목표다.
  N대가 동시에 그 부하를 지속한다는 뜻이 아니다.

#### 메모리 — GOMAXPROCS 에 비례한다

| 실행 환경 | 유휴 | 50 conn | 256 conn (qty=125) |
|---|---|---|---|
| 네이티브 macOS 프로세스 (GOMAXPROCS=10) | 5.3MB | 7.1MB | 9.2MB |
| 컨테이너, CPU 제한 없음 (GOMAXPROCS=10) | 5.01MiB | 9.82MiB | **15.22MiB** |
| 컨테이너, `--cpus 2` (GOMAXPROCS=2) | 4.90MiB | 5.85MiB | **9.24MiB** |

`docker stats` 는 cgroup `memory.current` 라 페이지 캐시를 포함하고, Go 런타임은
GOMAXPROCS 에 비례해 P별 캐시를 잡는다. **따라서 메모리 수치는 타깃과 같은
vCPU 수에서 측정한 값만 유효하다.** 네이티브 프로세스 RSS 로 컨테이너 사용량을
추정하면 안 된다.

Go 1.26 은 cgroup CPU 제한을 읽어 GOMAXPROCS 를 맞춘다 (측정: 제한 없음 → 10,
`--cpus 2` → 2, `--cpus 0.5` → 2, 하한 2). 2 vCPU 호스트라면 자동으로 2가 된다.

#### 로그 볼륨

`--log-level info` 에서 약 75만 요청을 처리한 뒤 로그는 **기동 줄 1개**가 전부였다.
커넥션 수립/종료는 `debug` 레벨이므로 정상 운용 중 로그는 사실상 증가하지 않는다.

---

## 8. 산출물

```
modbus_slave/
├── cmd/modbus-slave/main.go
├── internal/config/               설정 파싱 및 검증
├── internal/modbus/               MBAP 프레임, PDU, FC 디스패치 테이블
├── internal/server/               리스너, 커넥션 수명, 요청 루프
├── internal/health/               healthcheck 모드
├── internal/state/                실효 설정 상태 파일
├── slave.sh                       운영 진입점 (up/ps/net/top/restart/logs/down/update)
├── scripts/gen-compose.sh         포트 범위만큼 compose 생성
├── scripts/probe.go               단발 요청 확인용 최소 클라이언트
├── scripts/poll.go                §7.3 폴링 워크로드 재현 (make load)
├── scripts/smoke.sh               이미지 스모크 테스트 (make docker-test)
├── Dockerfile
├── docker-compose.example.yml     기능 데모용
├── docker-compose.deploy.yml      배포용 (§10.4 반영)
├── Makefile                       build / test / bench / load / docker-*
├── go.mod
├── README.md                      파라미터 표, 실행 예시, 지원 FC 표
└── PLAN.md                        (본 문서)
```

`scripts/*.go` 는 `//go:build ignore` 태그가 붙어 있어 `go build ./...` 와
이미지 빌드 컨텍스트에 들어가지 않는다. 측정 도구이지 배포 산출물이 아니다.

---

## 9. 작업 단계

| 단계 | 내용 | 완료 기준 | 상태 |
|---|---|---|---|
| 1 | 프로젝트 스캐폴딩, `go.mod`, 설정 파싱/검증 | `--port` 누락 시 exit 2, `--unit-id`/`--function-code` 기본값·파싱 테스트 통과 | ✅ 완료 |
| 2 | MBAP/PDU 코덱 + 단위 테스트 | 코덱 테스트 전부 통과 | ✅ 완료 |
| 3 | FC 디스패치 테이블 + FC03/04 핸들러 + 예외 처리 | 경계값·Unit ID 필터·비활성 FC 테스트 통과 | ✅ 완료 |
| 4 | TCP 서버 (accept 루프, 커넥션 goroutine, graceful shutdown) | 실제 소켓 통합 테스트 통과 | ✅ 완료 |
| 5 | Dockerfile + healthcheck 모드 | 이미지 10MB 미만, `docker ps` 에서 healthy | ✅ 완료 (4.01MB) |
| 6 | FC01/02/06/10 핸들러 추가 | 2차 테스트 통과 | ⛔ **범위 제외** (아래 참조) |
| 7 | compose 예제, README, Makefile | 문서만 보고 재현 가능 | ✅ 완료 |
| 8 | 부하 테스트 및 튜닝 | 7.3 성능 목표 달성 | ✅ 완료 (튜닝 불필요) |

1~5단계가 요구사항을 모두 충족하는 최소 완성본이고, 7~8 까지 완료했다.

#### 6단계를 제외한 이유 (2026-09-16 결정)

이 슬레이브의 용도는 **성능 테스트 전용**이며 마스터는 **FC03 만** 사용한다.
FC01/02/06/10 은 구현하지 않는다.

- 쓰지 않을 함수 코드를 구현하면 테스트 표면만 넓어진다
- `--function-code` 로 활성화할 수 없으므로(미구현 코드는 기동 실패) 오설정 위험도 없다
- 필요해지면 `internal/modbus/handler.go` 의 디스패치 테이블과
  `pdu.go` 의 `implemented` 목록에 등록하는 것으로 끝난다. 구조는 이미 열려 있다

FC04 는 이미 구현되어 있으나 기본 비활성이므로 그대로 둔다.

### 1차 구현 결과 (2026-09-15)

- `go test ./...` 전체 통과 (config 13 / modbus 8 / server 12 / health 7 케이스)
- `BenchmarkReadHoldingRegisters`: **11.3 ns/op, 0 B/op, 0 allocs/op** (125 레지스터 응답)
- 컨테이너 이미지 **4.01MB**, `docker stop` 응답 **0초**
- 컨테이너 레벨 확인 완료: `--port` 누락 exit 2 / 미구현 FC 기동 실패 /
  Unit ID 불일치 무응답 / 비활성 FC 예외 0x01 / 범위 밖 주소 예외 0x02 /
  FC04 인스턴스 healthy / 환경변수 설정

---

## 10. 배포 사이징 (EC2, 슬레이브 20개 기준)

§7.3 "실제 워크로드 재현" 실측을 근거로 한다.
전제: **컨테이너 20개, 컨테이너당 50 커넥션, 클라이언트마다 1초에 1000 레지스터 스캔**
= 컨테이너당 400 req/s, 전체 **8,000 req/s**.

### 10.1 CPU — 인스턴스 선택을 결정하는 유일한 변수

실측 0.168 vCPU(7,305 req/s)를 목표 8,000 req/s 로 환산하면 **0.184 vCPU** 다.
측정은 M-series P코어 위의 Linux VM 이므로, Graviton2 는 보수적으로 **2~2.5배**
느리다고 잡는다.

| 시나리오 | 측정(M-series) | Graviton2 환산 |
|---|---|---|
| 50 커넥션 / 8,000 req/s (현재) | 0.184 vCPU | **0.37 ~ 0.46 vCPU** |
| 100 커넥션 / 16,000 req/s (증설 시) | 0.473 vCPU | **0.95 ~ 1.18 vCPU** |

### 10.2 메모리 — 변수가 아니다

```
슬레이브 20 × 6MB        ≈  120 MB   (실측 5.0~5.8MiB, GOMAXPROCS=2 기준)
dockerd + containerd     ≈  200 MB
Amazon Linux 2023        ≈  250 MB
docker-proxy 20 × 3~10MB ≈  60~200 MB  ← 10.4 로 제거 가능
─────────────────────────────────────
합계                     ≈  630 MB ~ 790 MB
```

2GB 만 되면 충분하다. **인스턴스는 전적으로 vCPU 가 결정한다.**

> 주의: 슬레이브당 6MB 는 **GOMAXPROCS=2 기준**이다. vCPU 가 더 많은 호스트에
> 컨테이너별 CPU 제한 없이 올리면 런타임이 P별 캐시를 더 잡는다(§7.3).
> 3 vCPU 이상 인스턴스를 쓴다면 컨테이너마다 `cpus` 를 지정한다.

### 10.3 인스턴스 선택: **`c7g.large`**

**2 vCPU 전용(Graviton3) / 4GB.** 온디맨드 월 $50 내외(리전별 확인 필요).

| 시나리오 | c7g.large 2 vCPU 대비 사용률 |
|---|---|
| 50 커넥션 (현재) | 15 ~ 20% |
| 100 커넥션 | 35 ~ 48% |
| 200 커넥션 | 70 ~ 95% |

**버스터블(T 계열)을 배제하는 이유 — 산술 이전의 문제다.**
이 인스턴스의 용도는 성능 테스트다. T 계열은 CPU 크레딧이 소진되면 **조용히
스로틀링**되고, 그 순간 측정값 자체가 오염된다. 부하 테스트 결과를 믿을 수 없게
되는 것이 비용 절감보다 손해다.

산술로도 맞지 않는다. t4g.small 베이스라인은 vCPU 당 20% × 2 = **0.4 vCPU** 인데,
현재 50 커넥션 시나리오가 이미 0.37~0.46 으로 **경계선**이고,
100 커넥션이 되면 0.95~1.18 로 **2~3배 초과**한다.

| 대안 | 평가 |
|---|---|
| `c7g.medium` (1 vCPU, 2GB, 월 $26 내외) | 현재 50 커넥션은 30% 내외로 커버. 100 커넥션에서 70~95% 라 증설 여유 없음 |
| `c7g.xlarge` (4 vCPU) | 컨테이너를 40개 이상으로 늘리거나 커넥션 200 초과 시 |
| `t4g.small` / `t4g.medium` | ❌ 크레딧 스로틀링이 측정을 오염시킴. medium 은 베이스라인도 small 과 같은 20% 라 CPU 해결책이 아님 |
| `m7g.large` (2 vCPU, 8GB) | c7g.large 와 CPU 동일, 메모리만 2배. 메모리가 남으므로 불필요 |

### 10.4 배포 시 챙길 것

1. **docker-proxy 제거** — `/etc/docker/daemon.json` 에 `"userland-proxy": false` 를 넣는다.
   docker-proxy 프로세스가 전부 사라지면서 netns 격리와 `-p` 매핑 모델은 그대로 유지된다.

   > `network_mode: host` 로 바꾸는 방법도 있으나 **표준 포트 502 를 쓸 수 없게 된다**.
   > Docker 는 컨테이너 netns 에 `net.ipv4.ip_unprivileged_port_start=0` 을 넣어주므로
   > bridge 모드에서는 uid 65534 로도 502 를 열 수 있지만(실측 확인),
   > host 모드에서는 호스트 netns 의 1024 기준이 적용되어 `bind: permission denied` 가 난다
   > (실측 확인). host 모드를 쓴다면 `cap_add: NET_BIND_SERVICE` 를 함께 준다.

2. **`--max-conns`** — 기본 256 은 컨테이너당 50~100 커넥션에 충분하다.
   300 을 넘길 계획이면 올린다.

3. **컨테이너별 `mem_limit: 32m`** — 실측 5~6MiB 대비 5배 이상 여유.
   라이브 힙이 매우 작아 `GOMEMLIMIT` 까지는 필요 없다.

4. **로그 로테이션** (`max-size: 10m`, `max-file: 3`) — `info` 레벨에서는 로그가 거의
   늘지 않지만(실측: 75만 요청 후 1줄), `--log-level debug` 로 디버깅할 때를 대비한 상한이다.

5. **루트 볼륨 gp3 8GB** — 이미지가 4MB 라 충분하다.

6. **보안 그룹** — 사용할 포트 대역(슬레이브 20개면 502–521)만 열고 소스를 마스터 쪽 CIDR 로 제한한다.

7. **arm64 빌드** — `CGO_ENABLED=0` + `scratch` 라 Mac(arm64)에서 빌드한 이미지가
   c7g/t4g 에 그대로 올라간다. 다른 아키텍처에서 빌드한다면
   `docker buildx build --platform linux/arm64`.

8. **`ulimits` 는 건드리지 않는다** — 컨테이너 기본 soft nofile 이 1,048,576 이다(실측).
   `nofile: 65536` 을 지정하면 오히려 16배 낮아진다.

9. **클라이언트 쪽 `qty` 분할 확인** — 마스터가 1000 레지스터를 한 요청으로 보내면
   예외 0x03 을 받는다. 125개씩 8요청으로 나눠야 한다(§7.3).

위 항목을 반영한 compose 예시는 [docker-compose.deploy.yml](docker-compose.deploy.yml) 에 있다.

### 10.5 네트워크

트랜잭션당 패킷 2개. qty=125 응답은 MBAP 7 + PDU 252 + TCP/IP/Eth 54 = 약 313B,
요청은 약 66B.

| 전체 부하 | pps | 대역폭 |
|---|---|---|
| 8,000 req/s (50 커넥션) | 16,000 | 약 24 Mbps |
| 16,000 req/s (100 커넥션) | 32,000 | 약 49 Mbps |

c7g.large 베이스라인(약 0.781 Gbps)에서는 무시할 수준이다.
참고로 t4g.small 이었다면 베이스라인 0.128 Gbps 의 19~38% 를 쓰게 된다.

### 10.6 EC2 에서 다시 재야 할 것

위 수치는 전부 M-series + Docker Desktop 환경값이다. 실제 인스턴스에서 확인한다.

```bash
make load TARGETS=10.0.0.10:5020,10.0.0.10:5021 CONNS=50
```

- **CPU** — Graviton 환산 계수(2~2.5배)가 맞는지. 이게 인스턴스 선택의 유일한 근거다.
- **스캔 지연 p50/p99** — §7.3 의 124ms/141ms 는 측정 경로 값이라 EC2 값으로 대체해야 한다.
- **메모리** — 컨테이너당 5~6MiB 가 재현되는지.

---

## 11. 확정된 기본값

| 파라미터 | 기본값 | 미지정 시 동작 |
|---|---|---|
| `--port` | 없음 | **기동 실패** (exit 2) |
| `--registers` | 1000 | 주소 0 ~ 999 유효 |
| `--unit-id` | 1 | Unit ID 1 요청에만 응답, 그 외 무시 |
| `--function-code` | 0x03 | FC03만 응답, 나머지는 Illegal Function |

## 12. 범위 밖 (필요해지면 재논의)

성능 테스트 용도가 FC03 으로 확정되어 아래는 모두 범위 밖이다.

1. **FC01/02/06/10** — §9 6단계 참조.
2. **Holding / Input 레지스터 개수 분리** — 현재는 `--registers` 하나로 두 영역 모두
   동일하게 적용. 분리 필요 시 `--holding-registers` / `--input-registers` 추가.
3. **Coil / Discrete Input 개수** — 필요 시 `--coils` 파라미터 추가.
4. **Modbus RTU over TCP** — 현재 구현은 Modbus **TCP** 전용.

---

## 13. 배포

리눅스 서버 기동 절차는 [DEPLOY.md](DEPLOY.md) 참조.
