// Package health 는 Docker HEALTHCHECK 용 자가 점검을 수행한다.
package health

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"modbus-slave/internal/config"
	"modbus-slave/internal/modbus"
	"modbus-slave/internal/state"
)

const dialTimeout = 2 * time.Second

// Run 은 자기 자신에게 읽기 요청을 한 번 보내 정상 응답을 확인한다.
// 항상 FC03 을 쓰면 --function-code 4 로 띄운 인스턴스가 영구 unhealthy 가 되므로,
// 실제 설정된 Unit ID 와 활성 함수 코드를 사용한다.
func Run(cfg *config.Config) error {
	port := cfg.Port
	unitID := cfg.UnitID
	anyUnit := cfg.AnyUnitID
	fcs := cfg.FunctionCodes

	// 컨테이너의 HEALTHCHECK 는 CMD 인자를 볼 수 없다.
	// --port/MODBUS_PORT 가 없으면 기동 시 남긴 상태 파일에서 복원한다.
	if port == 0 {
		st, err := state.Read(cfg.StateFile)
		if err != nil {
			return fmt.Errorf("포트를 알 수 없음: --port 또는 MODBUS_PORT 를 주거나 상태 파일이 필요함 (%s): %w", cfg.StateFile, err)
		}
		port = st.Port
		unitID = st.UnitID
		anyUnit = st.AnyUnitID
		fcs = fcs[:0]
		for _, fc := range st.FunctionCodes {
			fcs = append(fcs, byte(fc))
		}
	}
	if port == 0 {
		return fmt.Errorf("포트를 알 수 없음")
	}
	if anyUnit {
		unitID = 1 // 전체 수락 모드에서는 아무 값이나 통과한다.
	}

	addr := net.JoinHostPort(dialHost(cfg.Bind), strconv.Itoa(port))
	c, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return fmt.Errorf("접속 실패 (%s): %w", addr, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(dialTimeout))

	fc, ok := firstReadFC(fcs)
	if !ok {
		// 읽기 함수가 하나도 활성화되지 않았으면 접속 가능 여부까지만 확인한다.
		return nil
	}

	req := make([]byte, modbus.HeaderLen+5)
	modbus.Header{TransactionID: 1, ProtocolID: 0, Length: 6, UnitID: unitID}.Encode(req)
	req[modbus.HeaderLen] = fc
	binary.BigEndian.PutUint16(req[modbus.HeaderLen+1:], 0) // start address 0
	binary.BigEndian.PutUint16(req[modbus.HeaderLen+3:], 1) // quantity 1
	if _, err := c.Write(req); err != nil {
		return fmt.Errorf("요청 전송 실패: %w", err)
	}

	var hdr [modbus.HeaderLen]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return fmt.Errorf("응답 헤더 수신 실패: %w", err)
	}
	h := modbus.DecodeHeader(hdr[:])
	if !h.Valid() {
		return fmt.Errorf("잘못된 응답 헤더 (protocol_id=%d length=%d)", h.ProtocolID, h.Length)
	}
	body := make([]byte, h.PDULen())
	if _, err := io.ReadFull(c, body); err != nil {
		return fmt.Errorf("응답 PDU 수신 실패: %w", err)
	}
	if body[0] != fc {
		return fmt.Errorf("예외 응답 수신 (function=0x%02X exception=0x%02X)", body[0], body[len(body)-1])
	}
	return nil
}

// dialHost 는 바인드 주소를 접속 가능한 주소로 바꾼다.
func dialHost(bind string) string {
	switch bind {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::", "[::]":
		return "::1"
	}
	return bind
}

func firstReadFC(fcs []byte) (byte, bool) {
	for _, fc := range fcs {
		if modbus.IsRead(fc) {
			return fc, true
		}
	}
	return 0, false
}
