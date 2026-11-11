package main

import (
	"errors"
	"net"
	"net/url"
)

func validatePlatform(endpoint string, allowLoopback, localDocker bool) error {
	u, e := url.Parse(endpoint)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return errors.New("SCARLET_URL无效")
	}
	if localDocker {
		if u.Scheme != "http" || u.Host != "app:8080" || (u.Path != "" && u.Path != "/") {
			return errors.New("本地Docker模式的平台仅允许http://app:8080")
		}
		return nil
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || !allowLoopback || ip == nil || !ip.IsLoopback() {
		return errors.New("平台连接要求HTTPS；本地开发须显式允许字面回环地址")
	}
	return nil
}
