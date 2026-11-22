package management

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

const hostInfoLimit = 192 * 1024

// 每列独立编码，保留主机名、挂载路径中的空格和非 ASCII 字符。
func parseHostInformation(raw []byte) (map[string]any, error) {
	bad := errors.New("HOST_INFO_INVALID")
	if len(raw) > hostInfoLimit || !utf8.Valid(raw) {
		return nil, bad
	}
	value := map[string]any{}
	for _, key := range strings.Fields("hostname osName osVersion kernel arch account privilege uptimeSeconds systemTime timezone") {
		value[key] = nil
	}
	value["cpu"] = map[string]any{"model": nil, "logicalCores": nil}
	value["memory"] = map[string]any{"totalBytes": nil, "availableBytes": nil}
	disks, interfaces := []map[string]any{}, []map[string]any{}
	seen := map[string]bool{}
	str := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	number := func(s string) any {
		if s == "" || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return nil
		}
		n, err := strconv.ParseUint(s, 10, 53)
		if err != nil {
			return nil
		}
		return n
	}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n") {
		if line == "" {
			continue
		}
		cols := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		kind := cols[0]
		count, ok := map[string]int{"system": 10, "cpu": 2, "memory": 2, "disk": 4, "network": 4}[kind]
		if !ok || len(cols) != count+1 {
			return nil, bad
		}
		fields := make([]string, count)
		for i, text := range cols[1:] {
			b, err := base64.StdEncoding.Strict().DecodeString(text)
			if err != nil || base64.StdEncoding.EncodeToString(b) != text || !utf8.Valid(b) || len(b) > 4096 || bytes.ContainsRune(b, 0) {
				return nil, bad
			}
			fields[i] = string(b)
		}
		if kind == "system" || kind == "cpu" || kind == "memory" {
			if seen[kind] {
				return nil, bad
			}
			seen[kind] = true
		}
		switch kind {
		case "system":
			for i, key := range strings.Fields("hostname osName osVersion kernel arch account privilege uptimeSeconds systemTime timezone") {
				value[key] = str(fields[i])
			}
			value["uptimeSeconds"] = number(fields[7])
			if fields[6] != "root" && fields[6] != "administrator" && fields[6] != "user" {
				value["privilege"] = nil
			}
		case "cpu":
			cores := number(fields[1])
			if cores == uint64(0) {
				cores = nil
			}
			value["cpu"] = map[string]any{"model": str(fields[0]), "logicalCores": cores}
		case "memory":
			value["memory"] = map[string]any{"totalBytes": number(fields[0]), "availableBytes": number(fields[1])}
		case "disk":
			if fields[0] == "" || len(disks) >= 64 {
				return nil, bad
			}
			disks = append(disks, map[string]any{
				"mount":          fields[0],
				"filesystem":     str(fields[1]),
				"totalBytes":     number(fields[2]),
				"availableBytes": number(fields[3]),
			})
		case "network":
			if fields[0] == "" || len(interfaces) >= 64 {
				return nil, bad
			}
			addresses := []string{}
			for _, address := range strings.Split(fields[3], "\n") {
				if address != "" {
					if len(address) > 128 {
						return nil, bad
					}
					addresses = append(addresses, address)
				}
			}
			if len(addresses) > 32 {
				return nil, bad
			}
			state := fields[2]
			if state != "up" && state != "down" {
				state = "unknown"
			}
			interfaces = append(interfaces, map[string]any{"name": fields[0], "mac": str(fields[1]), "state": state, "addresses": addresses})
		}
	}
	if !seen["system"] || value["hostname"] == nil {
		return nil, bad
	}
	value["disks"], value["interfaces"] = disks, interfaces
	return value, nil
}
