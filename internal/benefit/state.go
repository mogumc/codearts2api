package benefit

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// dateLayout 是签到状态里使用的本地日期格式。
const dateLayout = "2006-01-02"

// State 记录签到结果，供「每天首次请求时签到一次」做幂等与节流。
//
// 字段语义：
//   - LastSuccessDate：最后一次签到成功的本地日期，决定今天是否还需要签到；
//   - LastAttemptAt：最后一次尝试时间（含失败），用于失败后退避，避免每个请求都打上游；
//   - LastClaimTime：上游返回的 create_time（毫秒），便于排查；
//   - LastError：最后一次失败原因，成功后清空。
type State struct {
	LastSuccessDate string `json:"last_success_date,omitempty"`
	LastAttemptAt   int64  `json:"last_attempt_at,omitempty"`
	LastClaimTime   int64  `json:"last_claim_time,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

// LoadState 从磁盘读取签到状态；文件不存在或损坏时返回零值。
func LoadState(path string) State {
	if path == "" {
		return State{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}
	}
	return state
}

// Save 原子写入签到状态（先写临时文件再重命名，避免读到半截内容）。
func (s State) Save(path string) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	payload, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

// claimedOn 判断指定本地日期是否已签到成功。
func (s State) claimedOn(day string) bool {
	return s.LastSuccessDate == day
}
