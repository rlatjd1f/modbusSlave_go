#!/bin/sh
# modbus-slave 운영 스크립트. 저장소 어디에서 실행해도 저장소 루트 기준으로 동작한다.
#
#   ./slave.sh up 502-521        포트 502~521 에 슬레이브 20개 기동
#   ./slave.sh up 502            슬레이브 1개
#   ./slave.sh ps                상태 확인
#   ./slave.sh restart           전체 재시작
#   ./slave.sh restart slave-03  하나만 재시작
#   ./slave.sh logs slave-03     로그 따라가기
#   ./slave.sh down              전체 중지 및 삭제
#   ./slave.sh build             이미지 재빌드
#   ./slave.sh update            git pull + 재빌드 + 반영
#
# 환경변수로 조정: REGS(1000) MAXCONNS(256) MEMLIMIT(32m) IMAGE(modbus-slave)
#   REGS=2000 ./slave.sh up 502-521

set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

IMAGE="${IMAGE:-modbus-slave}"
FILE="docker-compose.yml"

die() {
	echo "오류: $*" >&2
	exit 1
}

usage() {
	# 파일 맨 위 주석 블록(첫 빈 줄까지)을 그대로 사용법으로 쓴다.
	sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
	exit "${1:-0}"
}

dc() {
	docker compose -f "$FILE" "$@"
}

need_compose() {
	[ -f "$FILE" ] || die "$FILE 이 없습니다. 먼저 './slave.sh up <포트범위>' 를 실행하세요."
}

is_num() {
	case "$1" in
	'' | *[!0-9]*) return 1 ;;
	*) return 0 ;;
	esac
}

# "502-521" 또는 "502" 를 START/END 로 분해한다.
parse_range() {
	case "$1" in
	*-*)
		START="${1%%-*}"
		END="${1##*-}"
		;;
	*)
		START="$1"
		END="$1"
		;;
	esac
	is_num "$START" && is_num "$END" || die "포트 범위 형식이 잘못됐습니다: '$1' (예: 502-521)"
	[ "$START" -ge 1 ] && [ "$END" -le 65535 ] || die "포트는 1~65535 범위여야 합니다: $START-$END"
	[ "$START" -le "$END" ] || die "시작 포트가 끝 포트보다 큽니다: $START-$END"
	COUNT=$((END - START + 1))
	[ "$COUNT" -le 200 ] || die "한 번에 200개까지만 지원합니다 (요청: $COUNT개)"
}

ensure_image() {
	if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
		echo "==> 이미지 '$IMAGE' 가 없어 빌드합니다"
		docker build -t "$IMAGE" .
	fi
}

# healthy 개수가 total 이 될 때까지 최대 60초 기다린다.
wait_healthy() {
	total="$1"
	i=0
	n=0
	while [ "$i" -lt 60 ]; do
		n=$(dc ps --format '{{.Status}}' 2>/dev/null | grep -c healthy || true)
		[ "$n" -ge "$total" ] && break
		sleep 1
		i=$((i + 1))
	done
	echo "==> healthy: $n / $total"
	[ "$n" -ge "$total" ] || {
		echo "    일부가 healthy 가 아닙니다. './slave.sh ps' 와 './slave.sh logs' 를 확인하세요." >&2
		exit 1
	}
}

cmd_up() {
	[ $# -ge 1 ] || die "포트 범위를 지정하세요. 예: ./slave.sh up 502-521"
	parse_range "$1"
	ensure_image
	echo "==> 슬레이브 ${COUNT}개 기동 (호스트 포트 ${START}~${END} -> 컨테이너 5020)"
	./scripts/gen-compose.sh "$COUNT" "$START" >"$FILE"
	# 범위를 줄였을 때 이전 컨테이너가 남지 않도록 한다.
	dc up -d --remove-orphans
	wait_healthy "$COUNT"
}

cmd_ps() {
	need_compose
	dc ps --format 'table {{.Name}}\t{{.Status}}\t{{.Ports}}'
	echo "==> healthy: $(dc ps --format '{{.Status}}' | grep -c healthy || true) / $(dc ps --format '{{.Name}}' | grep -c . || true)"
}

cmd_build() {
	echo "==> 이미지 빌드"
	docker build -t "$IMAGE" .
}

cmd_update() {
	echo "==> git pull"
	git pull
	cmd_build
	if [ -f "$FILE" ]; then
		echo "==> 새 이미지로 반영"
		dc up -d --remove-orphans
		wait_healthy "$(dc ps --format '{{.Name}}' | grep -c . || true)"
	else
		echo "    $FILE 이 없어 기동은 건너뜁니다. './slave.sh up <포트범위>' 를 실행하세요."
	fi
}

[ $# -ge 1 ] || usage 1
CMD="$1"
shift

case "$CMD" in
up) cmd_up "$@" ;;
ps | status) cmd_ps ;;
restart)
	need_compose
	dc restart "$@"
	wait_healthy "$(dc ps --format '{{.Name}}' | grep -c . || true)"
	;;
down)
	need_compose
	dc down --remove-orphans
	;;
logs)
	need_compose
	dc logs --tail 50 -f "$@"
	;;
build) cmd_build ;;
update) cmd_update ;;
-h | --help | help) usage 0 ;;
*) die "알 수 없는 명령: $CMD (./slave.sh --help)" ;;
esac
