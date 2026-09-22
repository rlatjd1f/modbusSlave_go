package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// collectorInfo 는 포트 하나가 속한 콜렉터다.
type collectorInfo struct {
	Collector string // 콜렉터 이름 (bas, bms, ...)
	Server    string // 콜렉터 서버 (collector-1, ...)
	ServerIP  string // 콜렉터 서버 내부 IP
}

// collectorMap 은 포트 번호로 콜렉터를 찾는다.
type collectorMap map[int]collectorInfo

// lookup 은 포트 문자열에 대응하는 콜렉터를 찾는다.
// 매핑이 없거나 포트가 목록에 없으면 두 번째 값이 false 다.
func (m collectorMap) lookup(port string) (collectorInfo, bool) {
	if m == nil {
		return collectorInfo{}, false
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return collectorInfo{}, false
	}
	c, ok := m[n]
	return c, ok
}

// loadCollectors 는 매핑 파일을 읽는다.
// 파일이 없으면 빈 매핑을 돌려준다. 라벨이 없을 뿐 동작에는 지장이 없다.
func loadCollectors(path string) (collectorMap, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return parseCollectors(f, path)
}

func parseCollectors(r interface{ Read([]byte) (int, error) }, name string) (collectorMap, error) {
	m := collectorMap{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		f := strings.Fields(text)
		if len(f) < 2 {
			return nil, fmt.Errorf("%s:%d: 최소 <콜렉터> <포트범위> 가 필요함: %q", name, line, text)
		}
		start, end, err := parsePortRange(f[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, line, err)
		}
		info := collectorInfo{Collector: f[0]}
		if len(f) > 2 {
			info.Server = f[2]
		}
		if len(f) > 3 {
			info.ServerIP = f[3]
		}
		for p := start; p <= end; p++ {
			if prev, dup := m[p]; dup {
				return nil, fmt.Errorf("%s:%d: 포트 %d 가 %q 와 %q 에 중복 지정됨",
					name, line, p, prev.Collector, info.Collector)
			}
			m[p] = info
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// parsePortRange 는 "502-505" 또는 "502" 를 해석한다.
func parsePortRange(s string) (int, int, error) {
	startStr, endStr, found := strings.Cut(s, "-")
	if !found {
		endStr = startStr
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(startStr))
	end, err2 := strconv.Atoi(strings.TrimSpace(endStr))
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("포트 범위 형식이 잘못됨: %q (예: 502-505)", s)
	}
	if start < 1 || end > 65535 || start > end {
		return 0, 0, fmt.Errorf("포트 범위가 유효하지 않음: %q", s)
	}
	return start, end, nil
}
