//go:build ignore

// probe 는 스모크 테스트용 최소 Modbus TCP 클라이언트다.
// 사용법: go run ./scripts/probe.go <host:port> [unit-id] [function-code] [start] [quantity]
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe <host:port> [unit-id] [fc] [start] [qty]")
		os.Exit(2)
	}
	addr := os.Args[1]
	unit := byte(argInt(2, 1))
	fc := byte(argInt(3, 3))
	start := uint16(argInt(4, 0))
	qty := uint16(argInt(5, 10))

	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	fatal(err)
	defer c.Close()
	fatal(c.SetDeadline(time.Now().Add(3 * time.Second)))

	req := make([]byte, 12)
	binary.BigEndian.PutUint16(req[0:2], 1) // transaction id
	binary.BigEndian.PutUint16(req[2:4], 0) // protocol id
	binary.BigEndian.PutUint16(req[4:6], 6) // length
	req[6] = unit
	req[7] = fc
	binary.BigEndian.PutUint16(req[8:10], start)
	binary.BigEndian.PutUint16(req[10:12], qty)
	_, err = c.Write(req)
	fatal(err)

	hdr := make([]byte, 7)
	_, err = io.ReadFull(c, hdr)
	fatal(err)
	n := int(binary.BigEndian.Uint16(hdr[4:6])) - 1
	pdu := make([]byte, n)
	_, err = io.ReadFull(c, pdu)
	fatal(err)

	if pdu[0]&0x80 != 0 {
		fmt.Fprintf(os.Stderr, "    예외 응답: function=0x%02X exception=0x%02X\n", pdu[0], pdu[1])
		os.Exit(1)
	}
	if int(pdu[1]) != int(qty)*2 || len(pdu) != 2+int(qty)*2 {
		fmt.Fprintf(os.Stderr, "    응답 길이 불일치: byte_count=%d pdu_len=%d\n", pdu[1], len(pdu))
		os.Exit(1)
	}
	for i, b := range pdu[2:] {
		if b != 0 {
			fmt.Fprintf(os.Stderr, "    레지스터 값이 0이 아님: offset %d = 0x%02X\n", i, b)
			os.Exit(1)
		}
	}
	fmt.Printf("    fc=0x%02X unit=%d start=%d qty=%d -> %d 바이트 전부 0\n", fc, unit, start, qty, pdu[1])
}

func argInt(i, def int) int {
	if len(os.Args) <= i {
		return def
	}
	n, err := strconv.ParseInt(os.Args[i], 0, 32)
	fatal(err)
	return int(n)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "    probe 실패:", err)
		os.Exit(1)
	}
}
