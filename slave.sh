#!/bin/sh
# modbus-slave 운영 스크립트. 저장소 어디에서 실행해도 저장소 루트 기준으로 동작한다.
#
#   ./slave.sh up 502-521        포트 502~521 에 슬레이브 20개 기동
#   ./slave.sh up 502            슬레이브 1개
#   ./slave.sh ps                상태 확인
#   ./slave.sh restart           전체 재시작
#   ./slave.sh restart slave-502 하나만 재시작 (서비스명 = slave-<포트>)
#   ./slave.sh logs slave-502    로그 따라가기
#   ./slave.sh net               아웃바운드 전송률(Mbps) + 커넥션 수 (기본 5초 샘플)
#   ./slave.sh net 10            10초 샘플
#   ./slave.sh top               CPU/메모리/송수신 실시간 갱신 (Ctrl+C 로 종료)
#   ./slave.sh top 5             5초 간격
#   ./slave.sh down              전체 중지 및 삭제
#   ./slave.sh build             이미지 재빌드
#   ./slave.sh update            git pull + 재빌드 + 반영
#   ./slave.sh mon up            모니터링 스택 기동 (Prometheus/Grafana/exporter/Redis 에이전트)
#                                퍼블릭 IP 로 열려면:
#                                GRAFANA_BIND=0.0.0.0 GRAFANA_PASSWORD='<비밀번호>' ./slave.sh mon up
#   ./slave.sh mon down          모니터링 스택 중지
#   ./slave.sh mon ps            모니터링 스택 상태
#   ./slave.sh mon logs          모니터링 스택 로그
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

# 이미지 존재 여부만 보면 git pull 로 소스가 바뀌어도 낡은 이미지가 그대로 쓰인다.
# 실제로 metrics-agent 가 예전 빌드로 남아 새 메트릭이 누락되는 일이 있었다.
# 매번 빌드하되 Docker 캐시가 있으므로 변경이 없으면 몇 초면 끝난다.
ensure_image() {
	echo "==> 이미지 확인 (변경 없으면 캐시로 즉시 끝난다)"
	docker build -t "$IMAGE" . >/dev/null
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

# 아웃바운드 전송률을 1회 출력한다.
# metrics-agent 가 떠 있으면 그 값을, 없으면 docker stats 두 표본의 차이를 쓴다.
cmd_net() {
	need_compose
	INT="${1:-5}"
	is_num "$INT" && [ "$INT" -ge 1 ] || die "샘플 시간은 1 이상의 정수여야 합니다: '$INT'"

	if snap=$(agent_snapshot) && [ -n "$snap" ]; then
		echo "==> 아웃바운드 전송률 (metrics-agent)"
		printf '%s\n' "$snap" | agent_rows | awk '
		{ printf "  %-26s %8.2f Mbps\n", $1, $5; sum += $5 }
		END { printf "  %-28s %8.2f Mbps\n", "합계", sum }'
	else
		echo "==> 아웃바운드 전송률 (${INT}초 샘플, docker stats)"
		echo "    주의: docker stats 는 유효숫자 3자리라 값이 거칠다. ./slave.sh mon up 을 띄우면 정확해진다." >&2
		names=$(dc ps --format '{{.Name}}')
		[ -n "$names" ] || die "실행 중인 컨테이너가 없습니다."
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
		NR <= n { t0[$1] = tob($4); next }
		{
			mbps = (tob($4) - t0[$1]) / t * 8 / 1000000
			printf "  %-26s %8.2f Mbps\n", $1, mbps
			sum += mbps
		}
		END { printf "  %-28s %8.2f Mbps\n", "합계", sum }'
	fi

	command -v nsenter >/dev/null 2>&1 || return 0
	if [ -n "$SUDO" ] && ! $SUDO -n true >/dev/null 2>&1; then
		echo "==> 커넥션 수는 건너뜁니다 (sudo 사용 불가)"
		return 0
	fi

	echo "==> 확립된 커넥션 수"
	total=0
	for c in $(dc ps --format '{{.Name}}'); do
		pid=$(docker inspect -f '{{.State.Pid}}' "$c" 2>/dev/null || echo "")
		[ -n "$pid" ] || continue
		cnt=$($SUDO nsenter -t "$pid" -n ss -tn state established 2>/dev/null | tail -n +2 | wc -l | tr -d ' ' || echo 0)
		printf "  %-26s %s\n" "$c" "$cnt"
		total=$((total + cnt))
	done
	printf "  %-28s %s\n" "합계" "$total"
}

# CPU / 메모리 / 인바운드 / 아웃바운드를 한 화면에서 주기적으로 갱신한다.
#
# metrics-agent 가 떠 있으면 그 값을 쓴다. Grafana 대시보드와 같은 소스라
# 두 화면의 숫자가 어긋나지 않는다. 에이전트가 없으면 docker stats 로 물러서는데,
# 그 경우 값이 거칠다는 점을 화면에 표시한다.
cmd_top() {
	need_compose
	INT="${1:-3}"
	is_num "$INT" && [ "$INT" -ge 1 ] || die "갱신 간격은 1 이상의 정수여야 합니다: '$INT'"

	trap 'printf "\n"; exit 0' INT TERM

	while :; do
		src="metrics-agent"
		if snap=$(agent_snapshot) && [ -n "$snap" ]; then
			rows=$(printf '%s\n' "$snap" | agent_rows)
		else
			src="docker stats (값 거칢)"
			names=$(dc ps --format '{{.Name}}' 2>/dev/null || true)
			if [ -z "$names" ]; then
				echo "실행 중인 컨테이너가 없습니다."
				sleep "$INT"
				continue
			fi
			# shellcheck disable=SC2086
			a=$(docker stats --no-stream --format '{{.Name}} {{.NetIO}}' $names 2>/dev/null || true)
			sleep "$INT"
			# shellcheck disable=SC2086
			b=$(docker stats --no-stream --format '{{.Name}} {{.NetIO}} {{.CPUPerc}} {{.MemUsage}}' $names 2>/dev/null || true)
			[ -n "$a" ] && [ -n "$b" ] || continue
			rows=$(printf '%s\n%s\n' "$a" "$b" | awk -v n="$(echo "$a" | wc -l)" -v t="$INT" '
			function tob(s,   v, u) {
				v = s + 0; u = s; sub(/^[0-9.]+/, "", u)
				if (u == "kB") return v * 1000
				if (u == "MB") return v * 1000000
				if (u == "GB") return v * 1000000000
				return v
			}
			NR <= n { r0[$1] = tob($2); t0[$1] = tob($4); next }
			{
				split($6, m, "MiB")
				printf "%s %.4f %.0f %.4f %.4f\n", $1, $5 + 0, m[1] * 1048576,
					(tob($2) - r0[$1]) / t * 8 / 1e6, (tob($4) - t0[$1]) / t * 8 / 1e6
			}')
		fi

		out=$(printf '%s\n' "$rows" | awk '
		function mib(b) { return b / 1048576 }
		{
			printf "  %-26s %7.2f%% %9.2fMiB %8.2f Mbps %8.2f Mbps\n", $1, $2, mib($3), $4, $5
			scpu += $2; smem += $3; srx += $4; stx += $5
		}
		END {
			printf "  %s\n", "--------------------------------------------------------------------------"
			printf "  %-28s %7.2f%% %9.2fMiB %8.2f Mbps %8.2f Mbps\n", "합계", scpu, mib(smem), srx, stx
			printf "  vCPU 환산 %.3f   |   통합 %.2f Mbps\n", scpu / 100, srx + stx
		}')

		clear 2>/dev/null || printf '\033[H\033[2J'
		printf '  %-26s %8s %12s %13s %13s\n' "CONTAINER" "CPU" "MEM" "IN" "OUT"
		printf '  %s\n' "--------------------------------------------------------------------------"
		printf '%s\n' "$out"
		printf '  %s   갱신 %ss   소스 %s   Ctrl+C 로 종료\n' "$(date '+%H:%M:%S')" "$INT" "$src"
		[ "$src" = "metrics-agent" ] && sleep "$INT"
	done
}

MONFILE="monitoring/docker-compose.yml"
AGENT_URL="${AGENT_URL:-http://127.0.0.1:9101/metrics}"

# metrics-agent 가 떠 있으면 그 값을 쓴다.
#
# docker stats 의 NET I/O 는 사람이 읽기 좋게 유효숫자 3자리로 반올림된 문자열이라
# ("266MB"), 누적이 커질수록 눈금이 거칠어진다. 누적 266MB 면 눈금이 1MB 이고
# 3초 창에서는 2.67 Mbps 단위로 양자화되어, 실제 1 Mbps 인 값이 0 또는 2.67 로만
# 찍힌다. 에이전트는 Docker API 의 원시 바이트 카운터를 읽으므로 그 문제가 없고,
# Grafana 대시보드와도 같은 값을 보게 된다.
agent_snapshot() {
	command -v curl >/dev/null 2>&1 || return 1
	curl -fsS --max-time 2 "$AGENT_URL" 2>/dev/null
}

# 에이전트 메트릭 텍스트를 "이름 CPU MEM IN OUT" 행으로 바꾼다.
agent_rows() {
	awk '
	function cname(s,   r) {
		if (!match(s, /container="[^"]+"/)) return ""
		r = substr(s, RSTART + 11, RLENGTH - 12)
		return r
	}
	function val(s) { return substr(s, index(s, "} ") + 2) + 0 }
	/^modbus_slave_transmit_mbps\{/ { c = cname($0); if (c != "") { tx[c] = val($0); seen[c] = 1 } }
	/^modbus_slave_receive_mbps\{/  { c = cname($0); if (c != "") { rx[c] = val($0); seen[c] = 1 } }
	/^modbus_slave_cpu_vcpu\{/      { c = cname($0); if (c != "") { cpu[c] = val($0); seen[c] = 1 } }
	/^modbus_slave_memory_bytes\{/  { c = cname($0); if (c != "") { mem[c] = val($0); seen[c] = 1 } }
	END {
		n = 0
		for (c in seen) names[++n] = c
		for (i = 1; i < n; i++)
			for (j = i + 1; j <= n; j++)
				if (names[i] > names[j]) { t = names[i]; names[i] = names[j]; names[j] = t }
		for (i = 1; i <= n; i++) {
			c = names[i]
			printf "%s %.4f %.0f %.4f %.4f\n", c, cpu[c] * 100, mem[c], rx[c], tx[c]
		}
	}'
}

# 모니터링 스택은 슬레이브와 별도 compose 로 둔다.
# 여기를 재시작해도 슬레이브 컨테이너가 흔들리지 않아야 한다.
cmd_mon() {
	[ -f "$MONFILE" ] || die "$MONFILE 이 없습니다."
	sub="${1:-ps}"
	[ $# -gt 0 ] && shift
	mdc() { docker compose -f "$MONFILE" "$@"; }

	case "$sub" in
	up)
		ensure_image
		bind="${GRAFANA_BIND:-0.0.0.0}"
		echo "==> 모니터링 스택 기동 (Grafana 바인드: $bind)"
		mdc up -d --remove-orphans

		# GF_SECURITY_ADMIN_PASSWORD 는 볼륨이 처음 만들어질 때만 적용된다.
		# 이미 만들어진 볼륨이 있으면 admin/admin 이 그대로 살아 있으므로,
		# 비밀번호가 지정되면 매번 강제로 다시 설정한다.
		if [ -n "${GRAFANA_PASSWORD:-}" ]; then
			i=0
			while [ "$i" -lt 30 ]; do
				if mdc exec -T grafana grafana cli admin reset-admin-password \
					"$GRAFANA_PASSWORD" >/dev/null 2>&1; then
					echo "==> Grafana 관리자 비밀번호 적용됨"
					break
				fi
				sleep 2
				i=$((i + 1))
			done
			[ "$i" -lt 30 ] || echo "  경고: 비밀번호 적용에 실패했습니다. 'mon logs grafana' 를 확인하세요." >&2
		fi
		echo
		if [ "$bind" = "127.0.0.1" ] || [ "$bind" = "localhost" ]; then
			echo "  Grafana     http://localhost:3000  (admin / ${GRAFANA_PASSWORD:-admin})"
			echo "  Prometheus  http://localhost:9090"
			echo
			echo "  SSH 터널로 접속: ssh -L 3000:localhost:3000 $USER@<서버IP>"
		else
			echo "  Grafana     http://<퍼블릭IP>:3000  (admin / ${GRAFANA_PASSWORD:-admin})"
			echo
			echo "  Grafana 만 외부에 열려 있다. Prometheus(9090)와 에이전트(9101)는"
			echo "  계속 127.0.0.1 전용이다."
			echo "  보안 그룹에서 TCP 3000 을 접속할 IP 대역으로 제한할 것."
		fi
		;;
	down) mdc down ;;
	ps | status) mdc ps ;;
	logs) mdc logs --tail 50 -f "$@" ;;
	restart) mdc restart "$@" ;;
	*) die "mon 하위 명령: up | down | ps | logs | restart (받은 값: $sub)" ;;
	esac
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
top) cmd_top "$@" ;;
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
mon) cmd_mon "$@" ;;
-h | --help | help) usage 0 ;;
*) die "알 수 없는 명령: $CMD (./slave.sh --help)" ;;
esac
