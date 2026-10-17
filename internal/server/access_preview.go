package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type serviceAccessRequest struct {
	InternalURL       string `json:"internal_url"`
	InternalDomainURL string `json:"internal_domain_url"`
	ExternalURL       string `json:"external_url"`
	SameHost          bool   `json:"same_host"`
}

type serviceAccessPreview struct {
	serviceAccessRequest
	InternalAccessURL string `json:"internal_access_url"`
}

func (a AccessConfig) usesHomeHost(raw string) bool {
	if a.HomeHost == "" || raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Hostname(), a.HomeHost)
}

// Same-host ownership is an editing intent; the stable URL remains the only saved value.
func (a AccessConfig) canonicalInternalURL(raw string, sameHost bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if !sameHost {
		return raw, nil
	}
	if a.HomeHost == "" {
		return "", fmt.Errorf("同机地址尚未配置，请使用服务自身地址")
	}
	if raw == "" {
		return "", fmt.Errorf("internal_url 不能为空")
	}
	if err := validateWebURL("internal_url", raw); err != nil {
		return "", err
	}
	u, _ := url.Parse(raw)
	return replaceAccessHost(u, u.Hostname(), a.HomeHost), nil
}

func (s *Server) handleAccessPreview(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	var draft serviceAccessRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&draft); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	cfg := s.currentConfig()
	internal, err := cfg.Access.canonicalInternalURL(draft.InternalURL, draft.SameHost)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	draft.InternalURL = internal
	draft.InternalDomainURL = strings.TrimSpace(draft.InternalDomainURL)
	draft.ExternalURL = strings.TrimSpace(draft.ExternalURL)
	for _, field := range []struct{ name, raw string }{{"internal_url", draft.InternalURL}, {"internal_domain_url", draft.InternalDomainURL}, {"external_url", draft.ExternalURL}} {
		if field.raw != "" {
			if err := validateWebURL(field.name, field.raw); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	draft.SameHost = cfg.Access.usesHomeHost(draft.InternalURL)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(serviceAccessPreview{serviceAccessRequest: draft, InternalAccessURL: cfg.Access.internalURL(draft.InternalURL, r.Host)})
}
