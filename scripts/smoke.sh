#!/bin/sh
# 빌드된 이미지가 실제로 Modbus 응답을 하는지 확인하는 스모크 테스트.
set -eu

IMAGE="${1:-modbus-slave}"
NAME="modbus-slave-smoke-$$"
PORT=15020

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> 컨테이너 기동 ($IMAGE)"
docker run -d --name "$NAME" -p "$PORT:5020" "$IMAGE" --port 5020 --registers 100 >/dev/null

echo "==> healthy 대기"
for i in $(seq 1 30); do
	status=$(docker inspect -f '{{.State.Health.Status}}' "$NAME" 2>/dev/null || echo starting)
	[ "$status" = "healthy" ] && break
	sleep 1
done
[ "$status" = "healthy" ] || { echo "FAIL: healthcheck 상태 = $status"; docker logs "$NAME"; exit 1; }
echo "    healthy"

echo "==> FC03 요청 검증"
go run ./scripts/probe.go "127.0.0.1:$PORT"

echo "==> 이미지 크기"
docker image inspect -f '    {{.Size}} bytes' "$IMAGE"

echo "PASS"
