package httpapi

import (
	"regexp"
	"strings"
)

// MaskIDNumber 证件号脱敏：保留前 4 位与后 2 位，中间以 * 代替。
func MaskIDNumber(s string) string {
	n := len([]rune(s))
	if n <= 6 {
		return strings.Repeat("*", n)
	}
	r := []rune(s)
	return string(r[:4]) + strings.Repeat("*", n-6) + string(r[n-2:])
}

// MaskPhone 手机号脱敏：保留前 3 位与后 4 位。
func MaskPhone(s string) string {
	n := len([]rune(s))
	if n < 7 {
		return strings.Repeat("*", n)
	}
	r := []rune(s)
	return string(r[:3]) + "****" + string(r[n-4:])
}

// idLikeRe 匹配日志中可能出现的 15~19 位证件号样式数字串（含末位 X）。
var idLikeRe = regexp.MustCompile(`\d{14,18}[0-9Xx]`)

// Redact 对任意待输出字符串做证件号脱敏，用于日志兜底。
func Redact(s string) string {
	return idLikeRe.ReplaceAllStringFunc(s, MaskIDNumber)
}
