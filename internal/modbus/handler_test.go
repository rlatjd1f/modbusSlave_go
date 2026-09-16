package modbus

import (
	"bytes"
	"testing"
)

func readPDU(fc byte, start, qty uint16) []byte {
	return []byte{fc, byte(start >> 8), byte(start), byte(qty >> 8), byte(qty)}
}

func TestReadRegistersReturnsZeros(t *testing.T) {
	h := NewHandler(1000, []byte{FCReadHoldingRegisters, FCReadInputRegisters})
	dst := make([]byte, MaxPDULen)

	for _, fc := range []byte{FCReadHoldingRegisters, FCReadInputRegisters} {
		for _, qty := range []uint16{1, 10, MaxReadQuantity} {
			resp := h.Handle(readPDU(fc, 0, qty), dst)
			wantLen := 2 + int(qty)*2
			if len(resp) != wantLen {
				t.Fatalf("fc=0x%02X qty=%d: 응답 길이 %d, want %d", fc, qty, len(resp), wantLen)
			}
			if resp[0] != fc {
				t.Fatalf("fc=0x%02X qty=%d: 함수 코드 0x%02X", fc, qty, resp[0])
			}
			if resp[1] != byte(qty*2) {
				t.Fatalf("fc=0x%02X qty=%d: byte count %d, want %d", fc, qty, resp[1], qty*2)
			}
			if !bytes.Equal(resp[2:], make([]byte, qty*2)) {
				t.Fatalf("fc=0x%02X qty=%d: 페이로드가 0이 아님: % X", fc, qty, resp[2:])
			}
		}
	}
}

func TestReadRegistersAddressBoundary(t *testing.T) {
	const registers = 1000
	h := NewHandler(registers, []byte{FCReadHoldingRegisters})
	dst := make([]byte, MaxPDULen)

	tests := []struct {
		name  string
		start uint16
		qty   uint16
		exc   Exception // 0 이면 정상 응답 기대
	}{
		{"첫 주소", 0, 1, 0},
		{"마지막 주소", registers - 1, 1, 0},
		{"범위 끝에 딱 맞음", registers - 10, 10, 0},
		{"범위 1 초과", registers - 10, 11, ExIllegalDataAddress},
		{"시작이 범위 밖", registers, 1, ExIllegalDataAddress},
		{"시작이 한참 밖", 0xFFFF, 1, ExIllegalDataAddress},
		{"quantity 0", 0, 0, ExIllegalDataValue},
		{"quantity 126", 0, MaxReadQuantity + 1, ExIllegalDataValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.Handle(readPDU(FCReadHoldingRegisters, tt.start, tt.qty), dst)
			if tt.exc == 0 {
				if resp[0]&ExceptionMask != 0 {
					t.Fatalf("예외 응답 0x%02X, 정상 응답 기대", resp[1])
				}
				return
			}
			if len(resp) != 2 {
				t.Fatalf("예외 응답 길이 %d, want 2", len(resp))
			}
			if resp[0] != FCReadHoldingRegisters|ExceptionMask {
				t.Fatalf("예외 함수 코드 0x%02X, want 0x%02X", resp[0], FCReadHoldingRegisters|ExceptionMask)
			}
			if Exception(resp[1]) != tt.exc {
				t.Fatalf("예외 코드 0x%02X, want 0x%02X", resp[1], byte(tt.exc))
			}
		})
	}
}

func TestInactiveFunctionCodeIsIllegal(t *testing.T) {
	// 기본 설정(FC03만 활성)에서 FC04 는 거절되어야 한다.
	h := NewHandler(1000, []byte{FCReadHoldingRegisters})
	dst := make([]byte, MaxPDULen)

	resp := h.Handle(readPDU(FCReadInputRegisters, 0, 1), dst)
	if resp[0] != FCReadInputRegisters|ExceptionMask || Exception(resp[1]) != ExIllegalFunction {
		t.Fatalf("FC04 응답 % X, want 예외 0x01", resp)
	}

	// 반대로 FC04만 활성이면 FC03 이 거절된다.
	h = NewHandler(1000, []byte{FCReadInputRegisters})
	resp = h.Handle(readPDU(FCReadHoldingRegisters, 0, 1), dst)
	if resp[0] != FCReadHoldingRegisters|ExceptionMask || Exception(resp[1]) != ExIllegalFunction {
		t.Fatalf("FC03 응답 % X, want 예외 0x01", resp)
	}
}

func TestUnknownFunctionCode(t *testing.T) {
	h := NewHandler(1000, []byte{FCReadHoldingRegisters})
	dst := make([]byte, MaxPDULen)
	resp := h.Handle([]byte{0x2B, 0x0E, 0x01, 0x00}, dst)
	if resp[0] != 0x2B|ExceptionMask || Exception(resp[1]) != ExIllegalFunction {
		t.Fatalf("응답 % X, want 예외 0x01", resp)
	}
}

func TestMalformedReadPDU(t *testing.T) {
	h := NewHandler(1000, []byte{FCReadHoldingRegisters})
	dst := make([]byte, MaxPDULen)
	for _, pdu := range [][]byte{
		{FCReadHoldingRegisters},
		{FCReadHoldingRegisters, 0x00, 0x00},
		{FCReadHoldingRegisters, 0x00, 0x00, 0x00, 0x01, 0xFF},
	} {
		resp := h.Handle(pdu, dst)
		if Exception(resp[1]) != ExIllegalDataValue {
			t.Fatalf("pdu % X: 응답 % X, want 예외 0x03", pdu, resp)
		}
	}
}

func TestEmptyPDU(t *testing.T) {
	h := NewHandler(1000, []byte{FCReadHoldingRegisters})
	if resp := h.Handle(nil, make([]byte, MaxPDULen)); resp != nil {
		t.Fatalf("빈 PDU 응답 % X, want nil", resp)
	}
}

// 응답 버퍼를 재사용해도 이전 응답 잔여물이 새 응답에 섞이지 않아야 한다.
func TestResponseBufferReuse(t *testing.T) {
	h := NewHandler(1000, []byte{FCReadHoldingRegisters})
	dst := make([]byte, MaxPDULen)

	h.Handle(readPDU(FCReadHoldingRegisters, 0, MaxReadQuantity), dst)
	h.Handle(readPDU(FCReadHoldingRegisters, 0, 0), dst) // 예외로 dst[1] 오염
	resp := h.Handle(readPDU(FCReadHoldingRegisters, 0, 5), dst)

	if resp[1] != 10 || !bytes.Equal(resp[2:], make([]byte, 10)) {
		t.Fatalf("재사용 후 응답 % X", resp)
	}
}

func BenchmarkReadHoldingRegisters(b *testing.B) {
	h := NewHandler(1000, []byte{FCReadHoldingRegisters})
	dst := make([]byte, MaxPDULen)
	pdu := readPDU(FCReadHoldingRegisters, 0, 125)
	b.ReportAllocs()
	for b.Loop() {
		h.Handle(pdu, dst)
	}
}
