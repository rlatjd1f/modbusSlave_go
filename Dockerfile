# syntax=docker/dockerfile:1

# --- build ---
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/modbus-slave ./cmd/modbus-slave
# 상태 파일 디렉터리. scratch 이미지에는 셸이 없어 런타임에 만들 수 없으므로
# 빌드 단계에서 비어 있는 디렉터리를 만들어 소유권과 함께 복사한다.
RUN mkdir -p /out/state && chown 65534:65534 /out/state

# --- runtime ---
FROM scratch
COPY --from=build /out/modbus-slave /modbus-slave
COPY --from=build --chown=65534:65534 /out/state /run/modbus

USER 65534:65534

# HEALTHCHECK 는 컨테이너의 CMD 인자를 볼 수 없다.
# 서버가 기동 시 이 파일에 실효 설정(포트/Unit ID/함수 코드)을 남기고,
# healthcheck 모드가 그것을 읽어 자기 자신에게 질의한다.
ENV MODBUS_STATE_FILE=/run/modbus/state.json

HEALTHCHECK --interval=30s --timeout=3s --start-period=2s --retries=3 \
    CMD ["/modbus-slave", "-healthcheck"]

ENTRYPOINT ["/modbus-slave"]
