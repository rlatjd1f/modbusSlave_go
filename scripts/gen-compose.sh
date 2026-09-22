#!/bin/sh
# 슬레이브 N개짜리 compose 파일을 만든다.
# usage: ./scripts/gen-compose.sh [개수] [시작 호스트 포트] > docker-compose.yml
#
# 호스트 포트는 Modbus 표준 포트 502 부터 할당한다. 1024 미만이지만 호스트 쪽
# 바인딩은 root 로 도는 dockerd 가 하므로 문제없다. 컨테이너 내부 포트는 항상
# 5020 으로 고정되어 비특권 uid(65534) 가 저번호 포트를 열 일이 없다.
set -eu

COUNT="${1:-20}"
START="${2:-502}"
REGS="${REGS:-1000}"
MAXCONNS="${MAXCONNS:-256}"
MEMLIMIT="${MEMLIMIT:-32m}"

cat <<HEADER
# scripts/gen-compose.sh 로 생성됨 (슬레이브 ${COUNT}개, 포트 ${START}~$((START+COUNT-1)))
# 설계 근거는 PLAN.md §10 참조.
#
# 사전 작업 — docker-proxy 제거:
#   /etc/docker/daemon.json 에 { "userland-proxy": false } 를 넣고
#   sudo systemctl restart docker

x-slave: &slave
  image: modbus-slave
  restart: unless-stopped
  mem_limit: ${MEMLIMIT}
  logging:
    driver: json-file
    options:
      max-size: "10m"
      max-file: "3"

services:
HEADER

# 서비스명과 컨테이너명에 호스트 포트를 넣는다.
# docker ps 만 봐도 어느 포트를 담당하는 슬레이브인지 바로 알 수 있다.
i=0
while [ "$i" -lt "$COUNT" ]; do
	port=$((START + i))
	cat <<SVC
  slave-${port}:
    <<: *slave
    container_name: modbus-slave-${port}
    command: ["--port", "5020", "--registers", "${REGS}", "--max-conns", "${MAXCONNS}"]
    ports: ["${port}:5020"]
SVC
	i=$((i + 1))
done
