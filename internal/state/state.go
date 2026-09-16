// Package state 는 기동한 slave 의 실효 설정을 파일로 남긴다.
// Docker HEALTHCHECK 는 컨테이너의 CMD 인자를 볼 수 없으므로,
// healthcheck 모드가 포트/Unit ID/함수 코드를 이 파일에서 복원한다.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State 는 상태 파일의 내용이다.
type State struct {
	PID           int       `json:"pid"`
	Bind          string    `json:"bind"`
	Port          int       `json:"port"`
	Registers     int       `json:"registers"`
	UnitID        uint8     `json:"unit_id"`
	AnyUnitID     bool      `json:"any_unit_id"`
	FunctionCodes []int     `json:"function_codes"`
	StartedAt     time.Time `json:"started_at"`
}

// Write 는 상태 파일을 기록한다. 실패해도 서비스는 계속되어야 하므로
// 호출부는 오류를 치명적으로 다루지 않는다.
func Write(path string, s State) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read 는 상태 파일을 읽는다.
func Read(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

// Remove 는 상태 파일을 지운다. 없으면 무시한다.
func Remove(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
