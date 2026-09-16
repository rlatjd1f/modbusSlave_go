//go:build ignore

// poll: 실제 폴링 패턴을 재현한다.
// 각 클라이언트가 interval 마다 registers 개를 전부 훑는다(FC03 125개씩 분할).
// usage: go run ./scripts/poll.go <host:port,...> <clients-per-target> <registers> <interval-ms> <seconds>
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxQty = 125

func main() {
	targets := strings.Split(os.Args[1], ",")
	perTarget, _ := strconv.Atoi(os.Args[2])
	registers, _ := strconv.Atoi(os.Args[3])
	intervalMS, _ := strconv.Atoi(os.Args[4])
	secs, _ := strconv.Atoi(os.Args[5])

	// 1000 레지스터 -> 125씩 8요청
	var chunks [][2]int
	for a := 0; a < registers; a += maxQty {
		q := registers - a
		if q > maxQty {
			q = maxQty
		}
		chunks = append(chunks, [2]int{a, q})
	}

	var reqs, scans, errs, overruns atomic.Int64
	var mu sync.Mutex
	var lat []time.Duration

	var wg sync.WaitGroup
	start := make(chan struct{})
	deadline := time.Duration(secs) * time.Second

	for _, tgt := range targets {
		for range perTarget {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				c, err := net.DialTimeout("tcp", addr, 10*time.Second)
				if err != nil {
					errs.Add(1)
					return
				}
				defer c.Close()
				req := make([]byte, 12)
				binary.BigEndian.PutUint16(req[4:6], 6)
				req[6] = 1
				req[7] = 3
				buf := make([]byte, 7+2+maxQty*2)
				local := make([]time.Duration, 0, secs*1000/intervalMS+8)

				<-start
				tick := time.NewTicker(time.Duration(intervalMS) * time.Millisecond)
				defer tick.Stop()
				end := time.Now().Add(deadline)
				for now := range tick.C {
					if now.After(end) {
						break
					}
					t0 := time.Now()
					for _, ch := range chunks {
						binary.BigEndian.PutUint16(req[8:10], uint16(ch[0]))
						binary.BigEndian.PutUint16(req[10:12], uint16(ch[1]))
						_ = c.SetDeadline(time.Now().Add(5 * time.Second))
						if _, err := c.Write(req); err != nil {
							errs.Add(1)
							return
						}
						if _, err := io.ReadFull(c, buf[:7+2+ch[1]*2]); err != nil {
							errs.Add(1)
							return
						}
						reqs.Add(1)
					}
					d := time.Since(t0)
					local = append(local, d)
					scans.Add(1)
					if d > time.Duration(intervalMS)*time.Millisecond {
						overruns.Add(1)
					}
				}
				mu.Lock()
				lat = append(lat, local...)
				mu.Unlock()
			}(tgt)
		}
	}

	time.Sleep(500 * time.Millisecond)
	t0 := time.Now()
	close(start)
	wg.Wait()
	el := time.Since(t0).Seconds()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		i := int(float64(len(lat)-1) * p)
		return lat[i]
	}
	fmt.Printf("  타깃 %d개 × 클라이언트 %d = 커넥션 %d, 레지스터 %d (요청 %d개/스캔)\n",
		len(targets), perTarget, len(targets)*perTarget, registers, len(chunks))
	fmt.Printf("  스캔 %d회, 요청 %d개, %.1fs -> %.0f req/s (에러 %d, 주기 초과 %d)\n",
		scans.Load(), reqs.Load(), el, float64(reqs.Load())/el, errs.Load(), overruns.Load())
	fmt.Printf("  1000레지스터 스캔 지연: p50 %v  p99 %v  max %v\n", pct(0.50), pct(0.99), pct(1.0))
}
