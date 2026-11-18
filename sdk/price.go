package sdk

type ModelPrice struct {
	Currency   string   `json:"currency"`
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cacheRead,omitempty"`
	CacheWrite *float64 `json:"cacheWrite,omitempty"`
}
