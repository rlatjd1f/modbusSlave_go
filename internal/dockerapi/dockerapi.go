// Package dockerapi 는 Docker 엔진 API 중 이 에이전트에 필요한 부분만 감싼다.
// 유닉스 소켓 위의 HTTP 라 net/http 만으로 충분하고 외부 의존성이 없다.
package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client 는 Docker 데몬 소켓에 붙는다.
type Client struct {
	http *http.Client
}

// New 는 주어진 유닉스 소켓을 쓰는 클라이언트를 만든다.
func New(socket string, timeout time.Duration) *Client {
	return &Client{
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// Container 는 목록 조회 결과 중 필요한 항목이다.
type Container struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
}

// Name 은 앞의 슬래시를 뗀 첫 번째 이름이다.
func (c Container) Name() string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// List 는 실행 중인 컨테이너 목록을 반환한다.
func (c *Client) List(ctx context.Context) ([]Container, error) {
	var out []Container
	if err := c.get(ctx, "/containers/json", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// statsResponse 는 /stats 응답 중 네트워크 부분만 본다.
type statsResponse struct {
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

// NetBytes 는 컨테이너의 모든 인터페이스를 합한 누적 수신/송신 바이트다.
// one-shot=true 로 요청해 데몬이 자체 델타를 계산하며 1초 기다리는 것을 피한다.
// 전송률 계산은 호출부가 두 표본의 차이로 직접 한다.
func (c *Client) NetBytes(ctx context.Context, id string) (rx, tx uint64, err error) {
	var s statsResponse
	if err := c.get(ctx, "/containers/"+id+"/stats?stream=false&one-shot=true", &s); err != nil {
		return 0, 0, err
	}
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	return rx, tx, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
