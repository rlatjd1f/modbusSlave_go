package modbus

import "testing"

func TestHeaderRoundTrip(t *testing.T) {
	in := Header{TransactionID: 0xABCD, ProtocolID: 0, Length: 6, UnitID: 17}
	var buf [HeaderLen]byte
	in.Encode(buf[:])
	got := DecodeHeader(buf[:])
	if got != in {
		t.Fatalf("왕복 실패: got %+v, want %+v", got, in)
	}
	want := [HeaderLen]byte{0xAB, 0xCD, 0x00, 0x00, 0x00, 0x06, 0x11}
	if buf != want {
		t.Fatalf("바이트 표현 불일치: got % X, want % X", buf, want)
	}
}

func TestHeaderValid(t *testing.T) {
	tests := []struct {
		name string
		h    Header
		want bool
	}{
		{"정상", Header{ProtocolID: 0, Length: 6}, true},
		{"최소 길이", Header{ProtocolID: 0, Length: MinLengthField}, true},
		{"최대 길이", Header{ProtocolID: 0, Length: MaxLengthField}, true},
		{"프로토콜 ID 불일치", Header{ProtocolID: 1, Length: 6}, false},
		{"길이 0", Header{ProtocolID: 0, Length: 0}, false},
		{"길이 1", Header{ProtocolID: 0, Length: 1}, false},
		{"길이 초과", Header{ProtocolID: 0, Length: MaxLengthField + 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.h.Valid(); got != tt.want {
				t.Fatalf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPDULen(t *testing.T) {
	if got := (Header{Length: 6}).PDULen(); got != 5 {
		t.Fatalf("PDULen() = %d, want 5", got)
	}
}
