package server

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"modbus-slave/internal/config"
	"modbus-slave/internal/modbus"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// start 는 임의 포트에 서버를 띄우고 주소와 정리 함수를 돌려준다.
func start(t *testing.T, mutate func(*config.Config)) (string, *Server) {
	t.Helper()
	cfg := &config.Config{
		Bind:          "127.0.0.1",
		Port:          0, // 임의 포트
		Registers:     1000,
		UnitID:        1,
		FunctionCodes: []byte{modbus.FCReadHoldingRegisters},
		MaxConns:      16,
		LogLevel:      "error",
	}
	if mutate != nil {
		mutate(cfg)
	}
	srv := New(cfg, discardLogger())
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-done
	})
	return srv.Addr().String(), srv
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}

// request 는 읽기 요청 ADU 를 만든다.
func request(txID uint16, unitID, fc byte, start, qty uint16) []byte {
	adu := make([]byte, modbus.HeaderLen+5)
	modbus.Header{TransactionID: txID, Length: 6, UnitID: unitID}.Encode(adu)
	adu[modbus.HeaderLen] = fc
	binary.BigEndian.PutUint16(adu[modbus.HeaderLen+1:], start)
	binary.BigEndian.PutUint16(adu[modbus.HeaderLen+3:], qty)
	return adu
}

// readResponse 는 응답 ADU 하나를 읽어 헤더와 PDU 를 돌려준다.
func readResponse(t *testing.T, c net.Conn) (modbus.Header, []byte) {
	t.Helper()
	var hdr [modbus.HeaderLen]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		t.Fatalf("응답 헤더 수신: %v", err)
	}
	h := modbus.DecodeHeader(hdr[:])
	if !h.Valid() {
		t.Fatalf("잘못된 응답 헤더: %+v", h)
	}
	pdu := make([]byte, h.PDULen())
	if _, err := io.ReadFull(c, pdu); err != nil {
		t.Fatalf("응답 PDU 수신: %v", err)
	}
	return h, pdu
}

func TestReadHoldingRegistersEndToEnd(t *testing.T) {
	addr, _ := start(t, nil)
	c := dial(t, addr)

	if _, err := c.Write(request(0x1234, 1, modbus.FCReadHoldingRegisters, 0, 10)); err != nil {
		t.Fatalf("요청 전송: %v", err)
	}
	h, pdu := readResponse(t, c)

	if h.TransactionID != 0x1234 {
		t.Errorf("TransactionID = 0x%04X, want 0x1234", h.TransactionID)
	}
	if h.UnitID != 1 {
		t.Errorf("UnitID = %d, want 1", h.UnitID)
	}
	if int(h.Length) != len(pdu)+1 {
		t.Errorf("Length = %d, PDU 길이 = %d", h.Length, len(pdu))
	}
	if pdu[0] != modbus.FCReadHoldingRegisters || pdu[1] != 20 || len(pdu) != 22 {
		t.Fatalf("응답 PDU % X", pdu)
	}
	for i, b := range pdu[2:] {
		if b != 0 {
			t.Fatalf("레지스터 값이 0이 아님: index %d = 0x%02X", i, b)
		}
	}
}

// 프레임을 1바이트씩 나눠 보내도 정확히 조립되어야 한다.
func TestSplitFrameIsReassembled(t *testing.T) {
	addr, _ := start(t, nil)
	c := dial(t, addr)

	req := request(7, 1, modbus.FCReadHoldingRegisters, 0, 1)
	for i := range req {
		if _, err := c.Write(req[i : i+1]); err != nil {
			t.Fatalf("바이트 %d 전송: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}
	h, pdu := readResponse(t, c)
	if h.TransactionID != 7 || pdu[0] != modbus.FCReadHoldingRegisters || pdu[1] != 2 {
		t.Fatalf("응답 header=%+v pdu=% X", h, pdu)
	}
}

// 한 번에 여러 요청을 밀어 넣어도 순서대로 전부 응답해야 한다.
func TestPipelinedRequests(t *testing.T) {
	addr, _ := start(t, nil)
	c := dial(t, addr)

	const n = 50
	var batch []byte
	for i := range n {
		batch = append(batch, request(uint16(i), 1, modbus.FCReadHoldingRegisters, 0, 1)...)
	}
	if _, err := c.Write(batch); err != nil {
		t.Fatalf("배치 전송: %v", err)
	}
	for i := range n {
		h, pdu := readResponse(t, c)
		if h.TransactionID != uint16(i) {
			t.Fatalf("%d번째 응답 TransactionID = %d", i, h.TransactionID)
		}
		if pdu[0] != modbus.FCReadHoldingRegisters {
			t.Fatalf("%d번째 응답 PDU % X", i, pdu)
		}
	}
}

// Unit ID 가 다르면 응답하지 않고, 커넥션은 유지되어야 한다.
func TestUnitIDMismatchIsSilentlyIgnored(t *testing.T) {
	addr, _ := start(t, nil)
	c := dial(t, addr)

	if _, err := c.Write(request(1, 2, modbus.FCReadHoldingRegisters, 0, 1)); err != nil {
		t.Fatalf("요청 전송: %v", err)
	}
	// 응답이 없어야 하므로 짧은 타임아웃으로 확인한다.
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var buf [1]byte
	if _, err := c.Read(buf[:]); err == nil {
		t.Fatal("Unit ID 불일치 요청에 응답함")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("타임아웃이 아닌 오류: %v (커넥션이 끊긴 것으로 보임)", err)
	}

	// 같은 커넥션으로 올바른 Unit ID 요청을 보내면 정상 응답해야 한다.
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(request(2, 1, modbus.FCReadHoldingRegisters, 0, 1)); err != nil {
		t.Fatalf("두 번째 요청 전송: %v", err)
	}
	h, pdu := readResponse(t, c)
	if h.TransactionID != 2 || pdu[0] != modbus.FCReadHoldingRegisters {
		t.Fatalf("두 번째 응답 header=%+v pdu=% X", h, pdu)
	}
}

func TestAnyUnitIDAcceptsEverything(t *testing.T) {
	addr, _ := start(t, func(c *config.Config) { c.UnitID = 0; c.AnyUnitID = true })
	c := dial(t, addr)

	for _, unit := range []byte{0, 1, 42, 255} {
		if _, err := c.Write(request(uint16(unit), unit, modbus.FCReadHoldingRegisters, 0, 1)); err != nil {
			t.Fatalf("unit %d 요청 전송: %v", unit, err)
		}
		h, pdu := readResponse(t, c)
		if h.UnitID != unit || pdu[0] != modbus.FCReadHoldingRegisters {
			t.Fatalf("unit %d: header=%+v pdu=% X", unit, h, pdu)
		}
	}
}

func TestInactiveFunctionCodeOverWire(t *testing.T) {
	addr, _ := start(t, nil) // FC03만 활성
	c := dial(t, addr)

	if _, err := c.Write(request(1, 1, modbus.FCReadInputRegisters, 0, 1)); err != nil {
		t.Fatalf("요청 전송: %v", err)
	}
	_, pdu := readResponse(t, c)
	if len(pdu) != 2 || pdu[0] != modbus.FCReadInputRegisters|modbus.ExceptionMask ||
		modbus.Exception(pdu[1]) != modbus.ExIllegalFunction {
		t.Fatalf("응답 PDU % X, want 예외 0x01", pdu)
	}
}

func TestOutOfRangeAddressOverWire(t *testing.T) {
	addr, _ := start(t, func(c *config.Config) { c.Registers = 100 })
	c := dial(t, addr)

	if _, err := c.Write(request(1, 1, modbus.FCReadHoldingRegisters, 99, 2)); err != nil {
		t.Fatalf("요청 전송: %v", err)
	}
	_, pdu := readResponse(t, c)
	if modbus.Exception(pdu[1]) != modbus.ExIllegalDataAddress {
		t.Fatalf("응답 PDU % X, want 예외 0x02", pdu)
	}
}

// Protocol ID 가 0이 아니면 Modbus 프레임이 아니므로 커넥션을 끊는다.
func TestBadProtocolIDClosesConnection(t *testing.T) {
	addr, _ := start(t, nil)
	c := dial(t, addr)

	req := request(1, 1, modbus.FCReadHoldingRegisters, 0, 1)
	binary.BigEndian.PutUint16(req[2:4], 1) // Protocol ID = 1
	if _, err := c.Write(req); err != nil {
		t.Fatalf("요청 전송: %v", err)
	}
	var buf [1]byte
	if _, err := c.Read(buf[:]); err != io.EOF {
		t.Fatalf("Read = %v, want io.EOF (커넥션 종료)", err)
	}
}

func TestConcurrentConnections(t *testing.T) {
	const clients, perClient = 32, 100
	addr, _ := start(t, func(c *config.Config) { c.MaxConns = clients })

	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			var hdr [modbus.HeaderLen]byte
			for i := range perClient {
				if _, err := c.Write(request(uint16(i), 1, modbus.FCReadHoldingRegisters, 0, 10)); err != nil {
					errs <- err
					return
				}
				if _, err := io.ReadFull(c, hdr[:]); err != nil {
					errs <- err
					return
				}
				h := modbus.DecodeHeader(hdr[:])
				pdu := make([]byte, h.PDULen())
				if _, err := io.ReadFull(c, pdu); err != nil {
					errs <- err
					return
				}
				if h.TransactionID != uint16(i) || pdu[1] != 20 {
					errs <- io.ErrUnexpectedEOF
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("동시 접속 테스트 실패: %v", err)
	}
}

func TestMaxConnsRejectsExcess(t *testing.T) {
	addr, _ := start(t, func(c *config.Config) { c.MaxConns = 1 })

	first := dial(t, addr)
	if _, err := first.Write(request(1, 1, modbus.FCReadHoldingRegisters, 0, 1)); err != nil {
		t.Fatalf("첫 커넥션 요청: %v", err)
	}
	readResponse(t, first) // 첫 커넥션이 슬롯을 점유한 것을 확인

	second, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("두 번째 Dial: %v", err)
	}
	defer second.Close()
	_ = second.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = second.Write(request(1, 1, modbus.FCReadHoldingRegisters, 0, 1))
	var buf [1]byte
	if _, err := second.Read(buf[:]); err == nil {
		t.Fatal("상한 초과 커넥션이 응답을 받음")
	}
}

func TestIdleTimeoutClosesConnection(t *testing.T) {
	addr, _ := start(t, func(c *config.Config) { c.IdleTimeout = 150 * time.Millisecond })
	c := dial(t, addr)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))

	var buf [1]byte
	if _, err := c.Read(buf[:]); err != io.EOF {
		t.Fatalf("Read = %v, want io.EOF (유휴 타임아웃 종료)", err)
	}
}

func TestShutdownStopsAccepting(t *testing.T) {
	addr, srv := start(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("Shutdown 이후에도 접속이 수락됨")
	}
}
