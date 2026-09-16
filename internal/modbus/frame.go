package modbus

import "encoding/binary"

// MBAP(Modbus Application Protocol) 프레임 상수.
const (
	HeaderLen = 7   // Transaction(2) + Protocol(2) + Length(2) + Unit(1)
	MaxPDULen = 253 // Modbus TCP PDU 최대 길이
	MaxADULen = HeaderLen + MaxPDULen

	// Length 필드는 Unit ID(1) + PDU 길이를 담는다.
	MinLengthField = 1 + 1
	MaxLengthField = 1 + MaxPDULen
)

// Header 는 MBAP 헤더다. 모든 다중 바이트 필드는 Big-Endian.
type Header struct {
	TransactionID uint16
	ProtocolID    uint16
	Length        uint16
	UnitID        uint8
}

// DecodeHeader 는 HeaderLen 바이트를 해석한다. b 는 최소 HeaderLen 길이여야 한다.
func DecodeHeader(b []byte) Header {
	return Header{
		TransactionID: binary.BigEndian.Uint16(b[0:2]),
		ProtocolID:    binary.BigEndian.Uint16(b[2:4]),
		Length:        binary.BigEndian.Uint16(b[4:6]),
		UnitID:        b[6],
	}
}

// Encode 는 헤더를 dst 앞 HeaderLen 바이트에 기록한다.
func (h Header) Encode(dst []byte) {
	binary.BigEndian.PutUint16(dst[0:2], h.TransactionID)
	binary.BigEndian.PutUint16(dst[2:4], h.ProtocolID)
	binary.BigEndian.PutUint16(dst[4:6], h.Length)
	dst[6] = h.UnitID
}

// Valid 는 Modbus TCP 프레임으로 볼 수 있는 헤더인지 판정한다.
// 거짓이면 프레임 동기화가 깨진 것이므로 커넥션을 끊어야 한다.
func (h Header) Valid() bool {
	return h.ProtocolID == 0 && h.Length >= MinLengthField && h.Length <= MaxLengthField
}

// PDULen 은 헤더 뒤에 이어질 PDU 바이트 수다. Valid 가 참일 때만 의미가 있다.
func (h Header) PDULen() int { return int(h.Length) - 1 }
