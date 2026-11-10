package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxAssets = 1000
	maxBytes  = 512 * 1024
)

type Asset struct {
	SourceRef string `json:"sourceRef"`
	Name      string `json:"name"`
}

type snapshot struct {
	Version  int     `json:"version"`
	Provider string  `json:"provider"`
	Source   string  `json:"source"`
	BatchID  string  `json:"batchId"`
	Assets   []Asset `json:"assets"`
}

func ValidSource(source string) bool {
	return validText(source, 80) && source == strings.TrimSpace(source)
}

// EncodeSnapshot validates the complete snapshot before it can be written to stdout.
func EncodeSnapshot(provider, source string, assets []Asset) ([]byte, error) {
	if provider != "postgresql" && provider != "openmetadata" {
		return nil, errors.New("不支持的采集来源")
	}
	if !ValidSource(source) {
		return nil, errors.New("来源别名须为 1 至 80 个可打印字符，首尾不含空白")
	}
	if len(assets) > maxAssets {
		return nil, errors.New("资产数量超过单批 1000 项上限")
	}

	rows := append(make([]Asset, 0, len(assets)), assets...)
	slices.SortFunc(rows, func(a, b Asset) int { return strings.Compare(a.SourceRef, b.SourceRef) })
	for i, row := range rows {
		if !validText(row.SourceRef, 500) || !validText(row.Name, 300) {
			return nil, errors.New("采集结果包含无效的资产标识或名称")
		}
		if i > 0 && rows[i-1].SourceRef == row.SourceRef {
			return nil, errors.New("采集结果包含重复的资产标识")
		}
	}

	result := snapshot{Version: 1, Provider: provider, Source: source, Assets: rows}
	content, err := json.Marshal(result)
	if err != nil {
		return nil, errors.New("无法生成资产快照")
	}
	sum := sha256.Sum256(content)
	result.BatchID = hex.EncodeToString(sum[:16])
	content, err = json.Marshal(result)
	if err != nil {
		return nil, errors.New("无法生成资产快照")
	}
	if len(content)+1 > maxBytes {
		return nil, errors.New("资产快照超过 512 KiB，请缩小采集范围")
	}
	return append(content, '\n'), nil
}

func validText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" &&
		utf8.RuneCountInString(value) <= maxRunes && strings.IndexFunc(value, func(r rune) bool {
		return !unicode.IsPrint(r)
	}) == -1
}

// Pin plaintext development connections to a literal loopback address.
func loopbackIP(host string) net.IP {
	if strings.EqualFold(host, "localhost") {
		return net.IPv4(127, 0, 0, 1)
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return ip
	}
	return nil
}
