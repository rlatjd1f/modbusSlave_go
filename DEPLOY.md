# 리눅스 서버 배포 가이드

Modbus 슬레이브 에뮬레이터를 리눅스 서버에 올리는 절차.
설계 근거와 인스턴스 사이징은 [PLAN.md](PLAN.md) §10 참조.

**전제**
- 대상: Amazon Linux 2023 / Ubuntu 22.04 이상, arm64 또는 x86_64
- 권장 인스턴스: `c7g.large` (2 vCPU 전용 / 4GB) — 근거는 PLAN.md §10.3
- 구성: 슬레이브 20개, 컨테이너당 50 커넥션, 각 클라이언트가 1초마다 1000 레지스터 스캔
- 함수 코드: **FC03 전용** (기본값이라 별도 지정 불필요)

> **서버에 Go 를 설치할 필요가 없다.** Dockerfile 이 멀티스테이지라
> 빌드는 `golang` 컨테이너 안에서 일어난다. Docker 와 git 만 있으면 된다.

---

## 1. Docker 설치

**Amazon Linux 2023**

```bash
sudo dnf install -y docker git
sudo systemctl enable --now docker
sudo usermod -aG docker $USER
```

**Ubuntu**

```bash
sudo apt update && sudo apt install -y docker.io docker-compose-v2 git
sudo systemctl enable --now docker
sudo usermod -aG docker $USER
```

`usermod` 후에는 **로그아웃했다가 다시 접속**해야 그룹이 적용된다.

```bash
docker version && docker compose version
```

---

## 2. docker-proxy 끄기

슬레이브 20개면 docker-proxy 프로세스가 20개 뜬다. 아래 설정으로 전부 없애면서
네트워크 격리와 `-p` 포트 매핑은 그대로 유지한다 (PLAN.md §10.4).

```bash
sudo mkdir -p /etc/docker
echo '{ "userland-proxy": false }' | sudo tee /etc/docker/daemon.json
sudo systemctl restart docker
```

> `network_mode: host` 로 바꾸지 말 것. 비특권 uid 로 실행하므로 표준 포트 502 를
> 열 수 없게 된다 (PLAN.md §10.4).

---

## 3. 코드 가져오기와 이미지 빌드

```bash
git clone <저장소 URL> modbus_slave
cd modbus_slave
docker build -t modbus-slave .
```

빌드는 1~2분 걸린다. 서버 아키텍처에 맞는 이미지가 자동으로 만들어진다.

```bash
docker image ls modbus-slave
```

> 다른 장비에서 빌드해 레지스트리로 옮긴다면 아키텍처를 명시한다.
> `docker buildx build --platform linux/arm64 -t modbus-slave .`

---

## 4. 슬레이브 기동

`slave.sh` 에 **포트 범위**를 주면 그만큼 컨테이너를 띄운다.
이미지가 없으면 알아서 빌드하고, 전부 healthy 가 될 때까지 기다린다.

```bash
./slave.sh up 502-521
```

```
==> 슬레이브 20개 기동 (호스트 포트 502~521 -> 컨테이너 5020)
==> healthy: 20 / 20
```

- 컨테이너 내부 포트는 전부 5020 이고 호스트 포트만 다르게 매핑된다
- 범위를 줄여서 다시 실행하면 남는 컨테이너는 자동으로 정리된다
- 포트 하나만 띄우려면 `./slave.sh up 502`

**502~521 은 1024 미만이지만 문제없다.** 호스트 쪽 바인딩은 root 로 도는 dockerd 가
하고, 컨테이너 안의 프로세스는 계속 5020 을 연다. 비특권 uid(65534)가 저번호 포트를
건드릴 일이 없도록 내부 포트를 5020 으로 고정해 둔 구조다.

레지스터 수나 커넥션 상한은 환경변수로 바꾼다.

```bash
REGS=2000 MAXCONNS=512 ./slave.sh up 502-541
```

| 환경변수 | 기본값 | 의미 |
|---|---|---|
| `REGS` | `1000` | 슬레이브당 레지스터 개수 |
| `MAXCONNS` | `256` | 슬레이브당 동시 접속 상한 |
| `MEMLIMIT` | `32m` | 컨테이너 메모리 상한 |
| `IMAGE` | `modbus-slave` | 사용할 이미지 이름 |

`slave.sh` 는 내부적으로 `scripts/gen-compose.sh` 로 `docker-compose.yml` 을 만든다.
compose 파일만 필요하면 그쪽을 직접 써도 된다.

---

## 5. 동작 확인

`STATUS` 가 전부 `Up ... (healthy)` 가 되어야 한다. healthcheck 는 슬레이브가
자기 설정대로 자신에게 FC03 질의를 보내 확인한다.

```bash
./slave.sh ps
```

```
NAME                      STATUS                   PORTS
modbus-slave-502   Up 5 seconds (healthy)   0.0.0.0:502->5020/tcp
...
==> healthy: 20 / 20
```

외부에서 실제 응답 확인:

```bash
mbpoll -m tcp -a 1 -r 1 -c 10 -p 502 <서버 IP>
```

> `ss -ltnp | grep 502` 로는 **아무것도 보이지 않는다.** §2 에서 `userland-proxy` 를
> 껐기 때문에 호스트에 리스닝 프로세스 없이 iptables DNAT 로만 처리된다.
> 고장이 아니므로 이걸로 판단하지 말고 위 `docker compose ps` 를 쓴다.
> 굳이 커널 쪽을 보려면 `sudo iptables -t nat -S DOCKER | grep -c 5020`.

`mbpoll` 이 없으면 아무 마스터 도구나 쓰면 된다. 기대값은 **레지스터 전부 0** 이다.

기동 로그:

```bash
docker compose logs --tail 3 slave-01
# level=INFO msg="modbus slave 시작" port=5020 registers=1000 unit_id=1 function_codes=0x03
```

---

## 6. 마스터 쪽에서 알아야 할 것

| 항목 | 값 |
|---|---|
| 프로토콜 | Modbus TCP |
| 포트 | **502 ~ 521** (슬레이브당 1개, 표준 Modbus 포트부터) |
| Unit ID | **1** (다른 값으로 보내면 **응답이 오지 않는다**) |
| 함수 코드 | **FC03** (Read Holding Registers). 다른 코드는 예외 0x01 |
| 레지스터 주소 | 0 ~ 999 |
| 반환 값 | 전부 0x0000 |

**FC03 은 한 요청에 최대 125개만 읽을 수 있다.**
1000 레지스터를 전부 훑으려면 **8요청으로 나눠야 한다.**
`qty=1000` 을 한 번에 보내면 예외 0x03 을 받는다.

즉 커넥션 50개가 1초마다 전 범위를 스캔하면 슬레이브당 **초당 400요청**이다.

---

## 7. 부하 측정

> **부하 생성기를 이 서버에서 돌리지 말 것.** 클라이언트와 슬레이브가 같은 CPU 를
> 나눠 쓰면 측정값이 오염된다. 마스터 쪽 장비나 별도 인스턴스에서 실행한다.

측정 장비에 Go 1.26 이상과 이 저장소가 있으면:

```bash
# 20개 슬레이브에 각각 50 커넥션, 1초마다 1000 레지스터 스캔, 60초
SERVER=10.0.0.10
TARGETS=$(seq 502 521 | sed "s/^/$SERVER:/" | paste -sd, -)
make load TARGETS=$TARGETS CONNS=50 SECS=60
```

출력 예시:

```
  타깃 20개 × 클라이언트 50 = 커넥션 1000, 레지스터 1000 (요청 8개/스캔)
  스캔 60000회, 요청 480000개, 60.0s -> 8000 req/s (에러 0, 주기 초과 0)
  1000레지스터 스캔 지연: p50 ...  p99 ...  max ...
```

**주기 초과**가 0이 아니면 스캔이 1초 예산을 넘긴 것이다. 슬레이브 CPU 부족인지
네트워크인지 아래로 확인한다.

슬레이브 쪽 네트워크와 커넥션 수는 서버에서 본다.

```bash
./slave.sh net        # 5초 샘플, 1회 출력
./slave.sh net 10     # 10초 샘플
./slave.sh top        # CPU/메모리/송수신 실시간 갱신 (Ctrl+C 로 종료)
./slave.sh top 5      # 5초 간격
```

`top` 은 부하를 거는 동안 띄워 두고 보는 용도다.

```
  CONTAINER                       CPU         MEM            IN           OUT
  --------------------------------------------------------------------------
  modbus-slave-502       0.56%    5.273MiB     0.31 Mbps     0.68 Mbps
  modbus-slave-503       0.60%     5.34MiB     0.38 Mbps     0.87 Mbps
  modbus-slave-504       0.34%    5.262MiB     0.38 Mbps     0.87 Mbps
  --------------------------------------------------------------------------
  합계                          1.50%     15.9MiB     1.07 Mbps     2.42 Mbps
  vCPU 환산 0.015   |   통합 3.50 Mbps   |   누적 수신 1.5 MB / 송신 3.3 MB
  10:23:26   갱신 3s   Ctrl+C 로 종료
```

- 컨테이너별 **IN / OUT 전송률**과 전체 **합계**, 그리고 컨테이너 기동 이후
  **누적 송수신량**을 함께 보여준다.
- `vCPU 환산` 은 CPU 합계를 코어 수로 바꾼 값이다. 인스턴스 사이징을 볼 때
  퍼센트보다 이쪽이 바로 읽힌다.
- OUT 이 IN 의 2~3배인 것이 정상이다. 요청은 12바이트지만 125개 레지스터 응답은
  259바이트다.

`docker stats` 로도 CPU 와 메모리는 실시간으로 볼 수 있다. 다만 `NET I/O` 가
누적값이라 전송률은 보이지 않는다.

```bash
docker stats --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}'
```

```
==> 아웃바운드 전송률 (5초 샘플)
  modbus-slave-502        0.73 Mbps
  modbus-slave-503        0.73 Mbps
  modbus-slave-504        0.83 Mbps
  modbus-slave-505        0.62 Mbps
  합계                           2.91 Mbps
==> 확립된 커넥션 수
  modbus-slave-502    50
  ...
  합계                       200
```

- **아웃바운드(TX)만** 표시한다. 슬레이브는 12바이트 요청을 받고 259바이트 응답을
  보내므로 나가는 쪽이 지배적이고, 인스턴스 대역폭 산정에도 이쪽이 기준이 된다.
- `docker stats` 의 `NET I/O` 는 **컨테이너 시작 이후 누적값**이라 그대로는 전송률이
  아니다. 이 명령은 두 번 재서 차이를 낸다.
- 커넥션 수는 마스터가 실제로 몇 개를 붙였는지 확인할 때 쓴다. 이미지가 `scratch` 라
  `docker exec` 로는 `ss` 를 쓸 수 없어, 호스트의 `ss` 를 컨테이너 네트워크
  네임스페이스에 넣어 실행한다. 이 부분에는 `sudo` 가 필요하며 스크립트가 알아서 붙인다.

CPU 와 메모리는 이렇게 본다.

```bash
docker stats --no-stream --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}' \
  | awk -F'|' '{split($2,m,"MiB"); split($3,c,"%"); ms+=m[1]; cs+=c[1]; n++} END {
      printf "메모리 합계 %.1f MiB (컨테이너당 %.2f), CPU 합계 %.1f%% -> %.3f vCPU\n",
      ms, ms/n, cs, cs/100 }'
```

기준값 (PLAN.md §7.3, M-series 측정):

| 컨테이너당 커넥션 | 총 req/s | CPU | 메모리 합계 |
|---|---|---|---|
| 50 | 8,000 | 0.168 vCPU | 약 100 MiB |
| 100 | 16,000 | 0.417 vCPU | 약 116 MiB |

Graviton 은 이보다 2~2.5배 높게 나올 것으로 예상한다. 실제 값이 이 범위를 크게
벗어나면 PLAN.md §10.1 의 환산 계수를 실측으로 갱신한다.

---

## 8. 모니터링과 Redis 연동

`monitoring/` 에 별도 compose 로 두었다. 슬레이브와 분리되어 있어 여기를
재시작해도 슬레이브 컨테이너는 흔들리지 않는다.

```bash
./slave.sh mon up      # 기동
./slave.sh mon ps      # 상태
./slave.sh mon logs    # 로그
./slave.sh mon down    # 중지
```

| 구성요소 | 역할 | 실측 메모리 |
|---|---|---|
| metrics-agent | Docker API 를 읽어 Redis 적재 + Prometheus 노출 | 8 MiB |
| cAdvisor | 컨테이너 CPU / 메모리 / 네트워크 | 24 MiB |
| node-exporter | 장비 CPU / 메모리 / NIC / 디스크 | 9 MiB |
| Prometheus | 수집·저장 (30일 / 최대 10GB) | 28 MiB |
| Grafana | 대시보드 | 80 MiB |

### Redis 연동

아웃바운드 전송률을 **3초마다** 해시 하나에 쓴다.

```
HSET liz.stats.server.modbus.network.traffic 502 0.86 503 0.87 ... total 41.20 ts 1758500000
EXPIRE liz.stats.server.modbus.network.traffic 9
```

- 필드 이름은 **호스트 포트**, 값은 Mbps 소수점 2자리
- `total` — 전체 통합 아웃바운드
- `ts` — 갱신 시각(unix). 소비하는 쪽이 신선도를 직접 판단할 수 있다
- **TTL 9초** (주기 3초 x 3). 에이전트가 죽으면 키가 사라지므로
  소비자가 낡은 값을 실시간 값으로 오인하지 않는다

Redis 주소 등은 환경변수로 바꾼다. 기본값은 `2mtest.liz.com:6379` 다.

```bash
REDIS_ADDR=other.host:6379 REDIS_PASSWORD=secret ./slave.sh mon up
```

| 환경변수 | 기본값 |
|---|---|
| `REDIS_ADDR` | `2mtest.liz.com:6379` |
| `REDIS_PASSWORD` | (없음) |
| `REDIS_DB` | `0` |
| `REDIS_KEY` | `liz.stats.server.modbus.network.traffic` |
| `GRAFANA_PASSWORD` | `admin` |

**Redis 가 끊겨도 슬레이브에는 영향이 없다.** 에이전트는 로그만 남기고 다음
주기에 재시도하며, 복구되면 `Redis 전송 복구` 를 남긴다.

```bash
docker exec -it <redis> redis-cli HGETALL liz.stats.server.modbus.network.traffic
```

### Grafana

**절대 인터넷에 열지 말 것.** Grafana(3000), Prometheus(9090), cAdvisor(8080),
node-exporter(9100), 에이전트(9101) 모두 `127.0.0.1` 에만 바인드했다.
원격에서 볼 때는 SSH 터널을 쓴다.

```bash
ssh -L 3000:localhost:3000 -L 9090:localhost:9090 ubuntu@<서버IP>
```

브라우저에서 `http://localhost:3000`, 초기 계정은 `admin / admin` 이다.
대시보드 `Modbus Slave 에뮬레이터` 가 자동으로 올라온다.

### 네트워크 측정을 두 경로로 두는 이유

통합 아웃바운드 패널은 **에이전트 값과 호스트 NIC 값을 함께** 그린다.

- 에이전트: Docker API 의 컨테이너별 송신 카운터
- node-exporter: 장비 NIC 의 실제 송신량

두 값이 크게 벌어지면 브리지·NAT 구간을 의심할 근거가 된다. 단일 소스만 보면
알 수 없다.

컨테이너별 네트워크를 **cAdvisor 가 아니라 에이전트에서** 받는 것도 의도적이다.
cAdvisor 는 호스트 cgroup 접근에 의존해 환경에 따라 컨테이너를 인식하지 못하는데,
에이전트는 Docker API 를 직접 읽으므로 그 영향을 받지 않는다. cAdvisor 는
CPU·메모리·디스크 쪽을 보완한다.

---

## 9. 운영

```bash
./slave.sh ps                 # 상태 + healthy 개수
./slave.sh net                # 아웃바운드 전송률(Mbps) + 커넥션 수
./slave.sh top                # CPU/메모리/송수신 실시간 갱신
./slave.sh restart            # 전체 재시작
./slave.sh restart slave-502  # 하나만 재시작 (서비스명 = slave-<포트>)
./slave.sh logs slave-502     # 로그 (info 레벨은 기동 줄 1개뿐)
./slave.sh down               # 전체 중지 및 삭제
./slave.sh up 502-521         # 다시 기동
```

**코드 갱신** — git pull, 재빌드, 반영을 한 번에 한다.

```bash
./slave.sh update
```

> `./slave.sh restart` 는 기존 컨테이너를 그대로 재시작하므로 **이미지를 새로
> 빌드했거나 포트 범위를 바꿨다면 반영되지 않는다.** 그럴 때는 `./slave.sh update`
> 나 `./slave.sh up <범위>` 를 쓴다.

**디버깅이 필요할 때만** 로그 레벨을 올린다. `debug` 는 커넥션 수립/종료를 전부
남기므로 커넥션이 많으면 로그가 빠르게 쌓인다.

```bash
docker run -d -p 502:5020 modbus-slave --port 5020 --log-level debug
```

**서버 재부팅** 시에는 `restart: unless-stopped` 와 `systemctl enable docker` 로
자동 복구된다. 별도 systemd 유닛은 필요 없다.

---

## 10. 방화벽 / 보안 그룹

```
인바운드 TCP 502-521  <- 마스터 쪽 CIDR 만 허용
인바운드 TCP 22       <- 관리 접속
```

AWS 보안 그룹이든 `firewalld` 든 **소스를 마스터 대역으로 제한**한다.
전체 개방하지 않는다.

---

## 11. 하지 말 것

| 항목 | 이유 |
|---|---|
| `ulimits: nofile` 지정 | 컨테이너 기본값이 1,048,576 이라 오히려 낮아진다 |
| `network_mode: host` | 비특권 uid 라 502 등 저번호 포트를 못 연다 |
| 이 서버에서 부하 생성기 실행 | 측정값 오염 |
| 버스터블(t4g) 인스턴스 사용 | CPU 크레딧 소진 시 조용히 스로틀링되어 측정이 망가진다 |
| `--log-level debug` 로 상시 운영 | 커넥션 로그가 쌓인다 |

---

## 12. 트러블슈팅

### 이미지 빌드 중 `network is unreachable` (IPv6)

```
dial tcp [2600:1f18:...]:443: connect: network is unreachable
```

Docker Hub 가 IPv6 주소로 응답했는데 인스턴스에 IPv6 경로가 없을 때 난다.
AWS 에서 서브넷에 IPv6 가 붙어 있고 egress-only 게이트웨이가 없으면 재현된다.
IPv4 는 멀쩡하기 때문에 일부 blob 요청만 실패하는 형태로 나타난다.

```bash
printf 'net.ipv6.conf.all.disable_ipv6 = 1\nnet.ipv6.conf.default.disable_ipv6 = 1\n' | sudo tee /etc/sysctl.d/99-disable-ipv6.conf
sudo sysctl --system
sudo systemctl restart docker
```

Go 로 작성된 dockerd 는 기동 시 IPv6 지원 여부를 확인하고, 스택이 꺼져 있으면
AAAA 주소를 시도하지 않는다. **데몬 재시작까지 해야 적용된다.**
적용 확인: `cat /proc/sys/net/ipv6/conf/all/disable_ipv6` 가 `1`.

### `permission denied ... /var/run/docker.sock`

docker 그룹이 아직 세션에 적용되지 않았다. SSH 를 끊고 다시 접속한다.

```bash
id -nG | tr ' ' '\n' | grep -x docker
```

`docker` 가 출력되지 않으면 재접속이 필요하다.

### 여러 줄 명령을 붙여넣을 때

`set -e` 를 대화형 셸에 붙여넣지 말 것. 명령 하나만 실패해도 **로그인 셸 자체가
종료되어 SSH 세션이 끊긴다.** 스크립트 파일에서만 쓴다.
붙여넣기용으로는 `&&` 로 연결한 한 줄을 쓴다.

### 포트가 열렸는지 `ss` 로 확인되지 않음

정상이다. §5 의 설명 참조.

---

## 13. 체크리스트

- [ ] Docker 설치 및 `usermod` 후 재접속
- [ ] `/etc/docker/daemon.json` 에 `userland-proxy: false`, Docker 재시작
- [ ] `git clone` 후 `docker build -t modbus-slave .`
- [ ] `./slave.sh up 502-521` 후 `healthy: 20 / 20`
- [ ] `./slave.sh ps` 로 `0.0.0.0:502->5020/tcp` 매핑 확인
- [ ] 보안 그룹에 502-521 개방 (소스 제한)
- [ ] 마스터가 **Unit ID 1 / FC03 / 125개 단위 분할**로 보내는지 확인
- [ ] 별도 장비에서 `make load` 실행, 주기 초과 0 확인
- [ ] `docker stats` 로 CPU·메모리가 기준값 범위인지 확인
- [ ] `./slave.sh mon up` 후 Prometheus 타깃 4개가 전부 `up`
- [ ] Redis 에 `liz.stats.server.modbus.network.traffic` 해시가 3초마다 갱신되는지
