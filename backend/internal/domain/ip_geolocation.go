package domain

// IPGeolocation 保存公网出口最近一次成功检测的地区和时区。
type IPGeolocation struct {
	IP          string `json:"ip"`           // 公网出口 IP。
	Country     string `json:"country"`      // 国家名称。
	CountryCode string `json:"country_code"` // 国家代码。
	Region      string `json:"region"`       // 州或省名称。
	City        string `json:"city"`         // 城市名称。
	Timezone    string `json:"timezone"`     // IANA 时区名称。
	DetectedAt  string `json:"detected_at"`  // 成功检测时间，使用 UTC RFC3339。
}
