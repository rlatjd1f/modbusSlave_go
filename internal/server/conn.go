package server

import (
	"bufio"
	"io"
	"net"
	"time"

	"modbus-slave/internal/modbus"
)

// readBufSize 는 커넥션당 읽기 버퍼다. 요청 ADU 가 12바이트 안팎이므로
// 파이프라이닝된 요청 여러 개를 한 번의 read 로 받아내기 충분한 크기면 된다.
const readBufSize = 1024

// handleConn 은 커넥션 하나의 요청 루프를 돈다.
// 버퍼는 커넥션당 한 번만 할당하고 요청마다 재사용한다.
func (s *Server) handleConn(c net.Conn) {
	defer c.Close()

	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}

	remote := c.RemoteAddr().String()
	s.log.Debug("커넥션 수립", "remote", remote)
	defer s.log.Debug("커넥션 종료", "remote", remote)

	r := bufio.NewReaderSize(c, readBufSize)
	var hdr [modbus.HeaderLen]byte
	req := make([]byte, modbus.MaxPDULen)
	resp := make([]byte, modbus.MaxADULen)

	for {
		if s.cfg.IdleTimeout > 0 {
			_ = c.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
		}
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err != io.EOF && !s.closing.Load() {
				s.log.Debug("헤더 수신 중단", "remote", remote, "err", err)
			}
			return
		}

		h := modbus.DecodeHeader(hdr[:])
		if !h.Valid() {
			// 프레임 동기화가 깨진 상태다. 이어서 읽어도 의미가 없으므로 끊는다.
			s.log.Debug("잘못된 MBAP 헤더로 커넥션 종료",
				"remote", remote, "protocol_id", h.ProtocolID, "length", h.Length)
			return
		}

		n := h.PDULen()
		if _, err := io.ReadFull(r, req[:n]); err != nil {
			s.log.Debug("PDU 수신 중단", "remote", remote, "err", err)
			return
		}

		// Unit ID 불일치는 다른 노드 앞으로 온 프레임으로 보고 응답하지 않는다.
		if !s.cfg.Accepts(h.UnitID) {
			s.log.Debug("Unit ID 불일치로 요청 무시",
				"remote", remote, "got", h.UnitID, "want", s.cfg.UnitID)
			continue
		}

		pdu := s.handler.Handle(req[:n], resp[modbus.HeaderLen:])
		if pdu == nil {
			continue
		}

		out := modbus.Header{
			TransactionID: h.TransactionID,
			ProtocolID:    0,
			Length:        uint16(len(pdu) + 1),
			UnitID:        h.UnitID,
		}
		out.Encode(resp[:modbus.HeaderLen])

		if s.cfg.IdleTimeout > 0 {
			_ = c.SetWriteDeadline(time.Now().Add(s.cfg.IdleTimeout))
		}
		if _, err := c.Write(resp[:modbus.HeaderLen+len(pdu)]); err != nil {
			s.log.Debug("응답 전송 실패", "remote", remote, "err", err)
			return
		}
	}
}
