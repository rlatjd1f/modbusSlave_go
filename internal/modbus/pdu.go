package modbus

import (
	"fmt"
	"strings"
)

// 함수 코드.
const (
	FCReadCoils              byte = 0x01
	FCReadDiscreteInputs     byte = 0x02
	FCReadHoldingRegisters   byte = 0x03
	FCReadInputRegisters     byte = 0x04
	FCWriteSingleRegister    byte = 0x06
	FCWriteMultipleRegisters byte = 0x10
)

// 예외 코드.
type Exception byte

const (
	ExIllegalFunction    Exception = 0x01
	ExIllegalDataAddress Exception = 0x02
	ExIllegalDataValue   Exception = 0x03
)

// 읽기 함수의 quantity 허용 범위.
const (
	MinReadQuantity = 1
	MaxReadQuantity = 125
)

// ExceptionMask 는 예외 응답에서 함수 코드에 OR 하는 비트다.
const ExceptionMask byte = 0x80

// implemented 는 현재 핸들러가 존재하는 함수 코드다.
// 여기에 없는 코드를 --function-code 로 지정하면 기동 단계에서 실패한다.
var implemented = []byte{
	FCReadHoldingRegisters,
	FCReadInputRegisters,
}

// IsImplemented 는 핸들러가 구현된 함수 코드인지 반환한다.
func IsImplemented(fc byte) bool {
	for _, c := range implemented {
		if c == fc {
			return true
		}
	}
	return false
}

// IsRead 는 읽기 계열 함수 코드인지 반환한다. healthcheck 가 쓸 코드를 고를 때 사용한다.
func IsRead(fc byte) bool {
	switch fc {
	case FCReadCoils, FCReadDiscreteInputs, FCReadHoldingRegisters, FCReadInputRegisters:
		return true
	}
	return false
}

// ImplementedList 는 오류 메시지에 쓸 구현 목록 문자열이다.
func ImplementedList() string {
	parts := make([]string, 0, len(implemented))
	for _, c := range implemented {
		parts = append(parts, fmt.Sprintf("0x%02X", c))
	}
	return strings.Join(parts, ", ")
}

// FunctionName 은 로그용 이름이다.
func FunctionName(fc byte) string {
	switch fc {
	case FCReadCoils:
		return "ReadCoils"
	case FCReadDiscreteInputs:
		return "ReadDiscreteInputs"
	case FCReadHoldingRegisters:
		return "ReadHoldingRegisters"
	case FCReadInputRegisters:
		return "ReadInputRegisters"
	case FCWriteSingleRegister:
		return "WriteSingleRegister"
	case FCWriteMultipleRegisters:
		return "WriteMultipleRegisters"
	}
	return fmt.Sprintf("Unknown(0x%02X)", fc)
}
