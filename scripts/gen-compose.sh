#!/bin/sh
# 슬레이브 N개짜리 compose 파일을 만든다.
# usage: ./scripts/gen-compose.sh [개수] [시작 호스트 포트] > docker-compose.yml
set -eu

COUNT="${1:-20}"
START="${2:-5020}"
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

i=0
while [ "$i" -lt "$COUNT" ]; do
	n=$(printf '%02d' $((i + 1)))
	port=$((START + i))
	cat <<SVC
  slave-${n}:
    <<: *slave
    command: ["--port", "5020", "--registers", "${REGS}", "--max-conns", "${MAXCONNS}"]
    ports: ["${port}:5020"]
SVC
	i=$((i + 1))
done
