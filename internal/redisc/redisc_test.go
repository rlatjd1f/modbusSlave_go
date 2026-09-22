package redisc

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func TestEncode(t *testing.T) {
	var b strings.Builder
	encode(&b, []string{"HSET", "k", "502", "0.86"})
	want := "*4\r\n$4\r\nHSET\r\n$1\r\nk\r\n$3\r\n502\r\n$4\r\n0.86\r\n"
	if b.String() != want {
		t.Fatalf("encode =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestReadReply(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		isErr bool
	}{
		{"simple string", "+OK\r\n", "OK", false},
		{"integer", ":3\r\n", "3", false},
		{"bulk", "$5\r\nhello\r\n", "hello", false},
		{"nil bulk", "$-1\r\n", "", false},
		{"array", "*2\r\n+a\r\n+b\r\n", "", false},
		{"error", "-ERR wrong number\r\n", "", true},
		{"unknown", "?oops\r\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readReply(bufio.NewReader(strings.NewReader(tt.in)))
			if tt.isErr {
				if err == nil {
					t.Fatalf("오류 없음, want 오류")
				}
				return
			}
			if err != nil {
				t.Fatalf("오류: %v", err)
			}
			if got != tt.want {
				t.Fatalf("= %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadReplyErrorCarriesMessage(t *testing.T) {
	_, err := readReply(bufio.NewReader(strings.NewReader("-NOAUTH Authentication required.\r\n")))
	if err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Fatalf("오류 = %v, want NOAUTH 포함", err)
	}
}

func TestIsAuthError(t *testing.T) {
	auth := []string{
		"NOAUTH Authentication required.",
		"WRONGPASS invalid username-password pair",
		"ERR Protocol error: unauthenticated multibulk length",
		"ERR invalid password",
	}
	for _, m := range auth {
		if !IsAuthError(errors.New(m)) {
			t.Errorf("IsAuthError(%q) = false, want true", m)
		}
	}
	other := []string{
		// 서버에 비밀번호가 없는데 AUTH 를 보낸 경우. 원문이 이미 설명적이라
		// 별도 문구로 감싸지 않고 그대로 보여준다.
		"ERR Client sent AUTH, but no password is set.",
		"ERR unknown command",
		"LOADING Redis is loading the dataset in memory",
		"connection refused",
	}
	for _, m := range other {
		if IsAuthError(errors.New(m)) {
			t.Errorf("IsAuthError(%q) = true, want false", m)
		}
	}
	if IsAuthError(nil) {
		t.Error("IsAuthError(nil) = true, want false")
	}
}
