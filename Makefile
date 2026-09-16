BINARY := modbus-slave
IMAGE  := modbus-slave
PORT   ?= 5020

# make load 기본값. PLAN.md §7.3 의 폴링 워크로드를 재현한다.
TARGETS  ?= 127.0.0.1:$(PORT)
CONNS    ?= 50
REGS     ?= 1000
INTERVAL ?= 1000
SECS     ?= 20

.PHONY: build test vet bench load probe run docker-build docker-run docker-test clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(BINARY) ./cmd/modbus-slave

test:
	go test ./...

vet:
	go vet ./...

bench:
	go test -bench=. -benchmem ./internal/modbus/

# 실제 폴링 패턴 부하 측정 (클라이언트마다 INTERVAL 마다 REGS 개를 125씩 나눠 스캔).
# 예: make load TARGETS=10.0.0.10:5020,10.0.0.10:5021 CONNS=50
load:
	go run ./scripts/poll.go $(TARGETS) $(CONNS) $(REGS) $(INTERVAL) $(SECS)

# 단발 요청 확인. 예: make probe TARGETS=127.0.0.1:5020
probe:
	go run ./scripts/probe.go $(TARGETS) 1 3 0 10

run: build
	./bin/$(BINARY) --port $(PORT)

docker-build:
	docker build -t $(IMAGE) .

docker-run: docker-build
	docker run --rm -p $(PORT):$(PORT) $(IMAGE) --port $(PORT)

# 이미지가 실제로 응답하는지 확인한다.
docker-test: docker-build
	./scripts/smoke.sh $(IMAGE)

clean:
	rm -rf bin
