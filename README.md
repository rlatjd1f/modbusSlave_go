# modbus-slave

모든 레지스터가 **0**을 반환하는 경량 Modbus TCP slave 시뮬레이터.
**컨테이너 1개 = slave 1개**로 동작한다.

- 이미지 크기 약 **4MB** (`scratch` 기반, non-root)
- 의존성 0 (Go 표준 라이브러리만 사용)
- 레지스터 규격: unsigned 16-bit (2 Byte), Big-Endian

---

## 빠른 시작

```bash
docker build -t modbus-slave .
docker run -d -p 5020:5020 modbus-slave --port 5020
```

여러 개를 한 번에 띄울 때는 포트 범위를 주면 된다. 이미지 빌드부터 healthy 대기까지
알아서 처리한다.

```bash
./slave.sh up 502-521      # 슬레이브 20개
./slave.sh ps              # 상태
./slave.sh net             # 아웃바운드 전송률(Mbps) + 커넥션 수
./slave.sh top             # CPU/메모리/송수신 실시간 갱신
./slave.sh restart         # 전체 재시작
./slave.sh down            # 전체 중지
```

```bash
mbpoll -m tcp -a 1 -r 1 -c 10 -p 5020 127.0.0.1
```

---

## 파라미터

| 플래그 | 환경변수 | 기본값 | 설명 |
|---|---|---|---|
| `--port` | `MODBUS_PORT` | **(필수)** | 리스닝 TCP 포트 (1~65535) |
| `--registers` | `MODBUS_REGISTERS` | `1000` | 레지스터 개수 (1~65536) |
| `--unit-id` | `MODBUS_UNIT_ID` | `1` | 응답할 Unit ID. `0` = 전체 수락 |
| `--function-code` | `MODBUS_FUNCTION_CODE` | `3` | 활성화할 함수 코드 (쉼표 구분) |
| `--bind` | `MODBUS_BIND` | `0.0.0.0` | 바인드 주소 |
| `--max-conns` | `MODBUS_MAX_CONNS` | `256` | 동시 접속 상한 |
| `--idle-timeout` | `MODBUS_IDLE_TIMEOUT` | `0` | 유휴 커넥션 종료 시간 (예: `30s`) |
| `--log-level` | `MODBUS_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `--state-file` | `MODBUS_STATE_FILE` | `/run/modbus/state.json` | healthcheck 가 읽는 상태 파일 |
| `--healthcheck` | - | - | 자가 점검 후 종료 (HEALTHCHECK 전용) |

우선순위는 **CLI 플래그 > 환경변수 > 기본값**이다.
`--port` 누락, 범위 밖 값, 미구현 함수 코드는 모두 **기동 실패(exit 2)** 로 처리한다.
잘못 설정된 컨테이너가 조용히 살아있지 않게 하기 위함이다.

---

## 함수 코드

`--function-code` 로 지정하며, **지정하지 않으면 0x03만 활성화**된다.
활성 목록에 없는 코드는 전부 Exception `0x01` (Illegal Function)로 응답한다.

| FC | 이름 | 상태 |
|---|---|---|
| `0x03` | Read Holding Registers | ✅ 구현 (기본 활성) |
| `0x04` | Read Input Registers | ✅ 구현 |
| `0x01` / `0x02` | Read Coils / Discrete Inputs | 미구현 |
| `0x06` / `0x10` | Write Single / Multiple Registers | 미구현 |

```bash
--function-code 3             # 기본값과 동일
--function-code 4             # FC04만 응답
--function-code 3,4           # 둘 다 응답
--function-code 0x03,0x04     # 0x 표기도 가능
```

### 예외 응답

| 조건 | 코드 |
|---|---|
| 비활성 / 미구현 함수 코드 | `0x01` Illegal Function |
| `quantity`가 1~125 밖이거나 PDU 길이가 잘못됨 | `0x03` Illegal Data Value |
| `start + quantity > --registers` | `0x02` Illegal Data Address |

**Unit ID 불일치**는 예외가 아니라 **무응답**이다 (Modbus TCP 스펙 권고).
게이트웨이 뒤 다른 노드 앞으로 온 프레임으로 간주하며, 커넥션은 유지한다.

---

## 실행 예시

```bash
# 기본 (레지스터 1000개, Unit ID 1, FC03)
docker run -d -p 5020:5020 modbus-slave --port 5020

# 레지스터 5000개
docker run -d -p 5021:5021 modbus-slave --port 5021 --registers 5000

# Unit ID 2, Input Register(FC04)로 응답
docker run -d -p 5022:5022 modbus-slave --port 5022 --unit-id 2 --function-code 4

# FC03 + FC04 동시 활성
docker run -d -p 5023:5023 modbus-slave --port 5023 --function-code 3,4

# 환경변수 방식
docker run -d -p 5024:5024 \
  -e MODBUS_PORT=5024 -e MODBUS_REGISTERS=200 -e MODBUS_UNIT_ID=1 modbus-slave
```

여러 개를 한 번에 띄우려면 [docker-compose.example.yml](docker-compose.example.yml) 참고.

```bash
docker compose -f docker-compose.example.yml up -d
```

`slave.sh` 가 만들어 주는 compose 파일은 메모리 상한과 로그 로테이션이 적용된
배포용 설정이다([docker-compose.deploy.yml](docker-compose.deploy.yml) 과 동일한 형태).
인스턴스 사이징 근거는 [PLAN.md](PLAN.md) §10 참고.

> 502 같은 특권 포트는 컨테이너 내부에서 쓰지 않고 `-p 502:5020` 으로 매핑한다.
> 컨테이너는 non-root(uid 65534)로 실행된다.

---

## HEALTHCHECK

Docker HEALTHCHECK 는 컨테이너의 CMD 인자를 볼 수 없다.
서버는 기동 시 실효 설정(포트 / Unit ID / 함수 코드)을 `--state-file` 에 남기고,
`--healthcheck` 모드가 그 값을 읽어 **자기 설정 그대로** 자신에게 질의한다.
덕분에 `--function-code 4` 로 띄운 인스턴스도 정상적으로 healthy 가 된다.

```bash
docker inspect -f '{{.State.Health.Status}}' <container>
```

---

## 개발

```bash
make build         # bin/modbus-slave 빌드
make test          # 전체 테스트
make bench         # 핸들러 벤치마크 (in-process)
make docker-build  # 이미지 빌드
make docker-test   # 이미지 스모크 테스트 (기동 → healthy → FC03 검증)
make run PORT=5020 # 로컬 실행
```

### 부하 측정

`make load` 는 실제 폴링 패턴을 재현한다. 클라이언트마다 주기적으로 레지스터
전 범위를 훑으며, **FC03 의 125개 제한 때문에 1000 레지스터 스캔은 8요청으로 나뉜다.**
즉 커넥션 50개는 초당 50요청이 아니라 **초당 400요청**이다.

```bash
# 기본: 127.0.0.1:5020 에 50 커넥션, 1초마다 1000 레지스터 스캔, 20초
make load

# 여러 슬레이브를 동시에
make load TARGETS=10.0.0.10:5020,10.0.0.10:5021 CONNS=50 SECS=60
```

| 변수 | 기본값 | 의미 |
|---|---|---|
| `TARGETS` | `127.0.0.1:5020` | 쉼표로 구분한 대상 목록 |
| `CONNS` | `50` | 대상당 클라이언트 수 |
| `REGS` | `1000` | 스캔할 레지스터 개수 |
| `INTERVAL` | `1000` | 스캔 주기 (ms) |
| `SECS` | `20` | 측정 시간 |

처리량, 스캔 지연 p50/p99, 주기 초과 횟수를 출력한다.
측정 결과와 해석은 [PLAN.md](PLAN.md) §7.3 참고.

단발 요청 확인:

```bash
make probe                                          # 기본 대상에 FC03 10개
go run ./scripts/probe.go 127.0.0.1:5020 1 3 0 10   # host:port unit fc start qty
```

> `scripts/*.go` 는 `//go:build ignore` 태그가 붙어 있어 `go build ./...` 와
> 이미지 빌드 컨텍스트에 들어가지 않는다.

### 구조

```
cmd/modbus-slave/   진입점, 플래그 처리, 시그널/graceful shutdown
internal/config/    설정 파싱 및 검증
internal/modbus/    MBAP 프레임, PDU, 함수 코드 디스패치 테이블
internal/server/    리스너, 커넥션 수명, 요청 루프
internal/health/    healthcheck 모드
internal/state/     실효 설정 상태 파일
```

리눅스 서버 배포 절차는 [DEPLOY.md](DEPLOY.md),
설계 배경과 사이징 근거는 [PLAN.md](PLAN.md) 참고.
