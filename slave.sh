#!/bin/sh
# modbus-slave 운영 스크립트. 저장소 어디에서 실행해도 저장소 루트 기준으로 동작한다.
#
#   ./slave.sh up 502-521        포트 502~521 에 슬레이브 20개 기동
#   ./slave.sh up 502            슬레이브 1개
#   ./slave.sh ps                상태 확인
#   ./slave.sh restart           전체 재시작
#   ./slave.sh restart slave-03  하나만 재시작
#   ./slave.sh logs slave-03     로그 따라가기
#   ./slave.sh net               네트워크 전송률 + 커넥션 수 (기본 5초 샘플)
#   ./slave.sh net 10            10초 샘플
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

# root 가 아니면 sudo 를 붙인다. 컨테이너 네임스페이스 진입에 필요하다.
if [ "$(id -u)" -eq 0 ]; then
	SUDO=""
else
	SUDO="sudo"
fi

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

# 누적 NetIO 를 두 번 재서 초당 전송률로 환산한다.
# docker stats 의 NET I/O 는 컨테이너 시작 이후 누적값이라 그대로는 전송률이 아니다.
cmd_net() {
	need_compose
	INT="${1:-5}"
	is_num "$INT" && [ "$INT" -ge 1 ] || die "샘플 시간은 1 이상의 정수여야 합니다: '$INT'"

	names=$(dc ps --format '{{.Name}}')
	[ -n "$names" ] || die "실행 중인 컨테이너가 없습니다."

	echo "==> 네트워크 전송률 (${INT}초 샘플)"
	# shellcheck disable=SC2086
	a=$(docker stats --no-stream --format '{{.Name}} {{.NetIO}}' $names)
	sleep "$INT"
	# shellcheck disable=SC2086
	b=$(docker stats --no-stream --format '{{.Name}} {{.NetIO}}' $names)

	printf '%s\n%s\n' "$a" "$b" | awk -v n="$(echo "$a" | wc -l)" -v t="$INT" '
	function tob(s,   v, u) {
		v = s + 0; u = s; sub(/^[0-9.]+/, "", u)
		if (u == "kB") return v * 1000
		if (u == "MB") return v * 1000000
		if (u == "GB") return v * 1000000000
		return v
	}
	function hum(x) {
		if (x >= 1000000) return sprintf("%.2f MB/s", x / 1000000)
		if (x >= 1000)    return sprintf("%.1f kB/s", x / 1000)
		return sprintf("%.0f B/s", x)
	}
	NR <= n { r0[$1] = tob($2); t0[$1] = tob($4); next }
	{
		rx = (tob($2) - r0[$1]) / t
		tx = (tob($4) - t0[$1]) / t
		printf "  %-26s RX %-12s TX %-12s %6.1f Mbps\n", $1, hum(rx), hum(tx), (rx + tx) * 8 / 1000000
		srx += rx; stx += tx
	}
	END {
		printf "  %-26s RX %-12s TX %-12s %6.1f Mbps\n", "합계", hum(srx), hum(stx), (srx + stx) * 8 / 1000000
	}'

	# 이미지가 scratch 라 docker exec 로는 ss 를 쓸 수 없다.
	# 호스트의 ss 를 컨테이너 네트워크 네임스페이스에 넣어 실행한다.
	command -v nsenter >/dev/null 2>&1 || return 0
	if ! $SUDO -n true >/dev/null 2>&1 && [ -n "$SUDO" ]; then
		echo "==> 커넥션 수는 건너뜁니다 (sudo 사용 불가)"
		return 0
	fi

	echo "==> 확립된 커넥션 수"
	total=0
	for c in $names; do
		pid=$(docker inspect -f '{{.State.Pid}}' "$c" 2>/dev/null || echo "")
		[ -n "$pid" ] || continue
		cnt=$($SUDO nsenter -t "$pid" -n ss -tn state established 2>/dev/null | tail -n +2 | wc -l | tr -d ' ' || echo 0)
		printf "  %-26s %s\n" "$c" "$cnt"
		total=$((total + cnt))
	done
	printf "  %-26s %s\n" "합계" "$total"
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
net) cmd_net "$@" ;;
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
