package modbus

import "encoding/binary"

// zeroPayload 는 모든 읽기 응답이 공유하는 0 바이트 원본이다.
// 값이 항상 0이므로 요청마다 페이로드를 새로 만들 이유가 없다.
var zeroPayload [MaxReadQuantity * 2]byte

type handlerFunc func(h *Handler, pdu, dst []byte) []byte

// Handler 는 활성 함수 코드별 응답 생성기를 256칸 배열로 펼쳐 둔다.
// 활성 여부 판정과 분기가 한 번의 인덱싱으로 끝나고,
// 비활성 코드는 자연스럽게 illegalFunction 을 가리킨다.
type Handler struct {
	registers int
	table     [256]handlerFunc
}

// NewHandler 는 registers 개의 주소를 가지며 fcs 에 있는 함수 코드에만 응답하는 핸들러를 만든다.
// registers 는 주소 범위 검증에만 쓰인다. 값이 전부 0이므로 저장 공간을 잡지 않는다.
func NewHandler(registers int, fcs []byte) *Handler {
	h := &Handler{registers: registers}
	for i := range h.table {
		h.table[i] = illegalFunction
	}
	for _, fc := range fcs {
		switch fc {
		case FCReadHoldingRegisters, FCReadInputRegisters:
			h.table[fc] = readRegisters
		}
	}
	return h
}

// Registers 는 설정된 레지스터 개수다.
func (h *Handler) Registers() int { return h.registers }

// Handle 은 요청 PDU 를 처리해 응답 PDU 를 dst 에 기록하고 그 슬라이스를 돌려준다.
// dst 는 MaxPDULen 이상이어야 한다. 응답할 것이 없으면 nil 을 반환한다.
func (h *Handler) Handle(pdu, dst []byte) []byte {
	if len(pdu) == 0 {
		return nil
	}
	return h.table[pdu[0]](h, pdu, dst)
}

// readRegisters 는 FC03/FC04 를 처리한다. 응답 값은 전부 0x0000.
func readRegisters(h *Handler, pdu, dst []byte) []byte {
	fc := pdu[0]
	if len(pdu) != 5 {
		return exception(fc, ExIllegalDataValue, dst)
	}
	start := binary.BigEndian.Uint16(pdu[1:3])
	qty := binary.BigEndian.Uint16(pdu[3:5])
	if qty < MinReadQuantity || qty > MaxReadQuantity {
		return exception(fc, ExIllegalDataValue, dst)
	}
	if int(start)+int(qty) > h.registers {
		return exception(fc, ExIllegalDataAddress, dst)
	}
	n := int(qty) * 2
	dst[0] = fc
	dst[1] = byte(n)
	copy(dst[2:2+n], zeroPayload[:n])
	return dst[:2+n]
}

func illegalFunction(_ *Handler, pdu, dst []byte) []byte {
	return exception(pdu[0], ExIllegalFunction, dst)
}

func exception(fc byte, code Exception, dst []byte) []byte {
	dst[0] = fc | ExceptionMask
	dst[1] = byte(code)
	return dst[:2]
}
