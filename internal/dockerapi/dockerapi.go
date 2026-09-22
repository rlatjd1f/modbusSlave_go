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

// statsResponse 는 /stats 응답 중 이 에이전트가 쓰는 항목만 본다.
type statsResponse struct {
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

// Stats 는 컨테이너 하나의 현재 누적값이다.
// 전송률과 CPU 사용량은 호출부가 두 표본의 차이로 직접 계산한다.
type Stats struct {
	RxBytes  uint64 // 수신 누적
	TxBytes  uint64 // 송신 누적
	CPUNanos uint64 // CPU 누적 사용 시간(ns)
	MemBytes uint64 // working set (usage - inactive_file)
}

// Stats 는 컨테이너의 네트워크/CPU/메모리 누적값을 한 번에 가져온다.
// one-shot=true 로 요청해 데몬이 자체 델타를 계산하며 1초 기다리는 것을 피한다.
func (c *Client) Stats(ctx context.Context, id string) (Stats, error) {
	var s statsResponse
	if err := c.get(ctx, "/containers/"+id+"/stats?stream=false&one-shot=true", &s); err != nil {
		return Stats{}, err
	}
	var out Stats
	for _, n := range s.Networks {
		out.RxBytes += n.RxBytes
		out.TxBytes += n.TxBytes
	}
	out.CPUNanos = s.CPUStats.CPUUsage.TotalUsage

	// cAdvisor 와 같은 기준으로 맞춘다. 페이지 캐시 중 회수 가능한 부분은 뺀다.
	out.MemBytes = s.MemoryStats.Usage
	if inactive, ok := s.MemoryStats.Stats["inactive_file"]; ok && inactive < out.MemBytes {
		out.MemBytes -= inactive
	}
	return out, nil
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
