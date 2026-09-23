package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
)

// 空回不扣费（empty response no charge）
//
// 上游偶发会返回「空回」：请求成功、但没有任何补全输出（输出 token 为 0）。
// 此时用户并没有拿到任何结果，仍然按输入 token 计费对用户不公平。
// 当配额设置的 EnableEmptyResponseNoCharge 开启时（默认开启），
// 这类请求一律不扣费：已预扣的额度会全额退回，消费日志配额记 0。
//
// 为避免误免单，本规则只作用于「本身就会产出补全内容」的中转格式：
// 向量（Embedding）、重排（Rerank）、图片生成、语音（TTS/转写/翻译）
// 以及实时语音（Realtime）等格式本来就没有补全输出或经常不上报用量，
// 必须排除在外，否则它们会全部变成免费。

// emptyResponseChargeableFormats 会产出补全内容、参与「空回不扣费」判定的中转格式。
var emptyResponseChargeableFormats = map[types.RelayFormat]struct{}{
	types.RelayFormatOpenAI:                    {},
	types.RelayFormatClaude:                    {},
	types.RelayFormatGemini:                    {},
	types.RelayFormatOpenAIResponses:           {},
	types.RelayFormatOpenAIResponsesCompaction: {},
}

// emptyResponseExcludedModes 即使属于上述格式也不参与判定的中转模式。
var emptyResponseExcludedModes = map[int]struct{}{
	relayconstant.RelayModeEmbeddings:         {},
	relayconstant.RelayModeRerank:             {},
	relayconstant.RelayModeModerations:        {},
	relayconstant.RelayModeImagesGenerations:  {},
	relayconstant.RelayModeImagesEdits:        {},
	relayconstant.RelayModeAudioSpeech:        {},
	relayconstant.RelayModeAudioTranscription: {},
	relayconstant.RelayModeAudioTranslation:   {},
}

// IsEmptyResponseChargeable 判断本次请求是否属于「有补全输出」的请求类型。
// 客户端请求格式与最终上游请求格式都必须在白名单内，任一不满足都不免单。
func IsEmptyResponseChargeable(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil {
		return false
	}
	if _, ok := emptyResponseExcludedModes[relayInfo.RelayMode]; ok {
		return false
	}
	if _, ok := emptyResponseChargeableFormats[relayInfo.RelayFormat]; !ok {
		return false
	}
	if _, ok := emptyResponseChargeableFormats[relayInfo.GetFinalRequestRelayFormat()]; !ok {
		return false
	}
	return true
}

// ShouldWaiveEmptyResponseQuota 判断本次请求是否命中「空回不扣费」。
// 条件：开关开启 + 请求类型会产出补全内容 + 输出 token 为 0。
func ShouldWaiveEmptyResponseQuota(relayInfo *relaycommon.RelayInfo, outputTokens int) bool {
	if outputTokens > 0 {
		return false
	}
	if !operation_setting.IsEmptyResponseNoChargeEnabled() {
		return false
	}
	return IsEmptyResponseChargeable(relayInfo)
}
