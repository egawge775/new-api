package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

type QuotaSetting struct {
	EnableFreeModelPreConsume bool `json:"enable_free_model_pre_consume"` // 是否对免费模型启用预消耗
	// 空回不扣费：上游返回的输出 token 为 0（空回）时，整个请求不扣费
	EnableEmptyResponseNoCharge bool `json:"enable_empty_response_no_charge"`
}

// 默认配置
var quotaSetting = QuotaSetting{
	EnableFreeModelPreConsume:   true,
	EnableEmptyResponseNoCharge: true,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("quota_setting", &quotaSetting)
}

func GetQuotaSetting() *QuotaSetting {
	return &quotaSetting
}

// IsEmptyResponseNoChargeEnabled 返回是否启用「空回不扣费」。
// 开启后，凡是会产生补全输出的请求，只要上游返回的输出 token 为 0，整个请求一律不扣费。
func IsEmptyResponseNoChargeEnabled() bool {
	return quotaSetting.EnableEmptyResponseNoCharge
}
