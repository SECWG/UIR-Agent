package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func ParseServer(server string) (*url.URL, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, errors.New("invalid server URL")
	}
	if u.User != nil ||
		u.RawQuery != "" ||
		u.ForceQuery ||
		u.Fragment != "" ||
		(u.Path != "" &&
			u.Path != "/") ||
		u.Hostname() == "" ||
		u.Opaque != "" {
		return nil, errors.New("server must be an origin without credentials, path or query")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("server must use HTTP or HTTPS")
	}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("invalid server port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("invalid server port")
	}
	return u, nil
}

// Protocol is a transport choice, not the namespace for a device's saved identity.
// Omitted ports still mean 80 for HTTP and 443 for HTTPS.
func ServerKey(server string) (string, error) {
	u, err := ParseServer(server)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	} else {
		n, _ := strconv.Atoi(port)
		port = strconv.Itoa(n)
	}
	return net.JoinHostPort(host, port), nil
}

func SameServer(a, b string) bool {
	left, err := ServerKey(a)
	if err != nil {
		return false
	}
	right, err := ServerKey(b)
	return err == nil && left == right
}

func directoryDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:8])
}

// Keep old physical directories in place, including their task history and locks.
// Never silently choose between two already-enrolled identities for the same address.
func DefaultDataDir(cacheRoot, server string) (string, error) {
	key, err := ServerKey(server)
	if err != nil {
		return "", err
	}
	root := filepath.Join(cacheRoot, "wg-ir-agent")
	name := directoryDigest(key)
	known := map[string]bool{name: true, directoryDigest(strings.TrimRight(server, "/")): true}
	host, port, _ := net.SplitHostPort(key)
	for _, scheme := range []string{"http", "https"} {
		known[directoryDigest(scheme+"://"+key)] = true
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			hostPart := host
			if strings.Contains(host, ":") {
				hostPart = "[" + host + "]"
			}
			known[directoryDigest(scheme+"://"+hostPart)] = true
		}
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Join(root, name), nil
	}
	if err != nil {
		return "", err
	}
	var candidates []string
	for _, entry := range entries {
		decoded, err := hex.DecodeString(entry.Name())
		if err != nil || len(decoded) != 8 {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if !entry.IsDir() {
			if known[entry.Name()] {
				return "", errors.New("Agent identity directory must be a regular directory")
			}
			continue
		}
		match := known[entry.Name()]
		if !match {
			info, statErr := os.Lstat(filepath.Join(path, "identity.json"))
			if statErr == nil && info.Mode().IsRegular() && info.Size() <= 8192 {
				raw, readErr := os.ReadFile(filepath.Join(path, "identity.json"))
				var id Identity
				match = readErr == nil && json.Unmarshal(raw, &id) == nil && SameServer(id.Server, server)
			}
		}
		if match {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) > 1 {
		return "", errors.New("multiple saved Agent identities exist for this host and port; choose the original --data-dir")
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return filepath.Join(root, name), nil
}
