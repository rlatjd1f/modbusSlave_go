// Package redisc 는 이 에이전트가 쓰는 최소한의 Redis 클라이언트다.
// RESP 명령 인코딩과 응답 파싱만 담으며, 외부 의존성을 들이지 않기 위해 직접 구현했다.
package redisc

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Client 는 Redis 연결 하나를 감싼다. 동시 사용은 고려하지 않는다.
type Client struct {
	addr     string
	password string
	db       int
	timeout  time.Duration

	conn net.Conn
	r    *bufio.Reader
}

// New 는 아직 연결하지 않은 클라이언트를 만든다.
func New(addr, password string, db int, timeout time.Duration) *Client {
	return &Client{addr: addr, password: password, db: db, timeout: timeout}
}

// Close 는 연결을 닫는다. 연결이 없으면 아무것도 하지 않는다.
func (c *Client) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
		c.r = nil
	}
}

// connect 는 필요할 때만 연결하고 AUTH/SELECT 까지 마친다.
func (c *Client) connect() error {
	if c.conn != nil {
		return nil
	}
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return fmt.Errorf("연결 실패: %w", err)
	}
	c.conn = conn
	c.r = bufio.NewReader(conn)
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))

	if c.password != "" {
		if _, err := c.do("AUTH", c.password); err != nil {
			c.Close()
			if IsAuthError(err) {
				return fmt.Errorf("Redis 비밀번호가 맞지 않습니다: %w", err)
			}
			return fmt.Errorf("AUTH 실패: %w", err)
		}
	}
	if c.db != 0 {
		if _, err := c.do("SELECT", strconv.Itoa(c.db)); err != nil {
			c.Close()
			return fmt.Errorf("SELECT %d 실패: %w", c.db, err)
		}
	}

	// 인증이 필요한데 비밀번호를 주지 않은 경우를 여기서 잡는다.
	// 인자가 많은 명령(HSET)으로 먼저 부딪히면 Redis 가
	// "Protocol error: unauthenticated multibulk length" 라는 알아보기 힘든
	// 오류를 돌려준다. 인자 하나짜리 PING 은 NOAUTH 를 그대로 보여준다.
	if _, err := c.do("PING"); err != nil {
		c.Close()
		if IsAuthError(err) {
			return fmt.Errorf("Redis 가 인증을 요구합니다. REDIS_PASSWORD 를 지정하세요 (%w)", err)
		}
		return fmt.Errorf("PING 실패: %w", err)
	}
	return nil
}

// IsAuthError 는 인증 때문에 거부된 응답인지 판정한다.
// Redis 는 상황에 따라 서로 다른 문구를 쓴다.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	for _, k := range []string{
		"NOAUTH",          // 인증 필요
		"WRONGPASS",       // 비밀번호 불일치
		"UNAUTHENTICATED", // 인증 전 다중 인자 명령 거부
		"INVALID PASSWORD",
	} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// Publish 는 필드 맵을 해시에 쓰고 TTL 을 건다.
// HSET 과 EXPIRE 를 한 번에 보내고 응답 두 개를 읽는 파이프라인이다.
// 어떤 단계든 실패하면 연결을 버려서 다음 호출이 새로 연결하게 한다.
func (c *Client) Publish(key string, fields []string, ttl time.Duration) error {
	if len(fields) == 0 || len(fields)%2 != 0 {
		return errors.New("필드는 이름/값 쌍이어야 함")
	}
	if err := c.connect(); err != nil {
		return err
	}

	args := make([]string, 0, len(fields)+2)
	args = append(args, "HSET", key)
	args = append(args, fields...)

	var buf strings.Builder
	encode(&buf, args)
	encode(&buf, []string{"EXPIRE", key, strconv.Itoa(int(ttl.Seconds()))})

	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	if _, err := c.conn.Write([]byte(buf.String())); err != nil {
		c.Close()
		return fmt.Errorf("전송 실패: %w", err)
	}
	for range 2 {
		if _, err := readReply(c.r); err != nil {
			c.Close()
			return fmt.Errorf("응답 오류: %w", err)
		}
	}
	return nil
}

func (c *Client) do(args ...string) (string, error) {
	var buf strings.Builder
	encode(&buf, args)
	if _, err := c.conn.Write([]byte(buf.String())); err != nil {
		return "", err
	}
	return readReply(c.r)
}

// encode 는 인자 목록을 RESP 배열로 만든다.
func encode(w *strings.Builder, args []string) {
	fmt.Fprintf(w, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(w, "$%d\r\n%s\r\n", len(a), a)
	}
}

// readReply 는 응답 하나를 읽는다. 값 자체는 쓰지 않으므로 형태만 소비한다.
func readReply(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("빈 응답")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", errors.New(line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", fmt.Errorf("잘못된 bulk 길이: %q", line)
		}
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2) // 본문 + CRLF
		if _, err := readFull(r, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", fmt.Errorf("잘못된 배열 길이: %q", line)
		}
		for i := 0; i < n; i++ {
			if _, err := readReply(r); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("알 수 없는 응답 형식: %q", line)
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
