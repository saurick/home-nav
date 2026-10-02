package server

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

//go:embed web/index.html web/app.css web/app.js
var uiFiles embed.FS

var indexTemplate = string(uiFile("web/index.html"))
var uiAssetVersion = fmt.Sprintf("%x", sha256.Sum256(append(uiFile("web/app.css"), uiFile("web/app.js")...)))[:12]

func uiFile(name string) []byte {
	body, err := uiFiles.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return body
}

func handleUIAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	if name != "app.css" && name != "app.js" {
		http.NotFound(w, r)
		return
	}
	contentType := "text/css; charset=utf-8"
	if name == "app.js" {
		contentType = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	etag := `"` + uiAssetVersion + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	if r.URL.Query().Get("v") == uiAssetVersion {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(uiFile("web/" + name))
	}
}

type navigationService struct {
	ID string `json:"id"`
	ServiceUpdateRequest
}

type navigationGroup struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Services []navigationService `json:"services"`
}

type navigationData struct {
	Groups     []navigationGroup `json:"groups"`
	Appearance Appearance        `json:"appearance"`
}

// Only navigation fields cross this boundary; auth and local storage paths stay on the server.
func navigationState(cfg *Config) navigationData {
	data := navigationData{Groups: make([]navigationGroup, 0, len(cfg.Groups)), Appearance: cfg.Appearance}
	for _, group := range cfg.Groups {
		view := navigationGroup{ID: group.ID, Name: group.Name, Services: make([]navigationService, 0, len(group.Services))}
		for _, service := range group.Services {
			pinned := service.Pinned
			view.Services = append(view.Services, navigationService{ID: service.ID, ServiceUpdateRequest: ServiceUpdateRequest{
				Name: service.Name, Description: service.Description, IconText: service.IconText, Icon: service.Icon,
				InternalURL: service.InternalURL, InternalDomainURL: service.InternalDomainURL, ExternalURL: service.ExternalURL,
				Tags: append([]string{}, service.Tags...), Notes: service.Notes, GroupID: group.ID, Pinned: &pinned,
				Health: ServiceHealthUpdateRequest{Type: service.Health.Type, URL: service.Health.URL, Address: service.Health.Address,
					ExpectStatus: service.Health.ExpectStatus, Timeout: service.Health.Timeout.String()},
			}})
		}
		data.Groups = append(data.Groups, view)
	}
	return data
}

func navigationJSON(cfg *Config) template.JS {
	body, _ := json.Marshal(navigationState(cfg))
	return template.JS(body)
}

func (s *Server) handleNavigation(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(navigationState(s.currentConfig()))
}

func userError(message string) (string, string) {
	rules := []struct{ match, text, field string }{
		{"至少需要配置一个", "请至少填写一个访问地址。", "external_url"},
		{"health.timeout", "请输入大于零的超时时间，例如 2s 或 500ms。", "health_timeout"},
		{"health.expect_status", "请输入 100–599 之间的状态码。", "health_expect_status"},
		{"health.url", "请填写有效的网页探测地址，以 http:// 或 https:// 开头。", "health_url"},
		{"health.address", "请填写端口探测地址，例如 192.168.1.10:443。", "health_address"},
		{"health.type", "请选择一种监测方式。", "health_type"},
		{"internal_domain_url", "请填写有效的内网域名地址，以 http:// 或 https:// 开头。", "internal_domain_url"},
		{"internal_url", "请填写有效的内网 IP 地址，以 http:// 或 https:// 开头。", "internal_url"},
		{"external_url", "请填写有效的外网地址，以 http:// 或 https:// 开头。", "external_url"},
		{"appearance.background_color", "请输入六位颜色值，例如 #18212b。", "background_color"},
		{"appearance.background_image", "请选择图库图片，或填写有效的图片地址。", "background_image"},
		{"appearance.background_overlay", "请选择一种壁纸遮罩强度。", "background_overlay"},
		{"name 不能为空", "请填写名称。", "name"},
		{"tags[", "标签不能为空，请移除空标签。", "tags"},
		{"写入临时配置失败", "配置无法保存，请检查配置文件的写入权限。", ""},
		{"替换配置失败", "配置无法保存，请检查配置文件的写入权限。", ""},
		{"未配置上传目录", "图片存储尚未启用，请先配置上传位置。", ""},
		{"读取图库失败", "图库暂时无法读取，请检查图片存储位置后重试。", ""},
		{"排序数据", "入口或分组列表已变化，请重新读取列表后调整顺序。", ""},
		{"服务不存在", "该入口已被移除，请重新读取列表。", ""},
		{"分组不存在", "该分组已被移除，请重新读取列表。", "group_id"},
		{"配置错误", "配置未通过检查，请检查填写内容后重试。", ""},
	}
	for _, rule := range rules {
		if strings.Contains(message, rule.match) {
			return rule.text, rule.field
		}
	}
	if strings.Contains(message, "permission denied") || strings.Contains(message, "no such file") {
		return "无法读取或保存文件，请检查存储位置和写入权限。", ""
	}
	switch message {
	case "请先登录", "请求方法不支持", "请求内容无效", "至少需要保留一个分组", "分组下还有入口，请先移动或删除入口",
		"请选择图片文件", "读取图片失败", "图片不能为空", "图片不能超过 8MB", "生成文件名失败", "创建上传目录失败", "保存图片失败",
		"请选择要删除的资源", "资源正在使用中，请先从背景或入口图标中移除", "资源不存在", "读取资源失败", "不能删除目录", "只能删除图片资源", "删除资源失败",
		"仅支持 PNG、JPG、WEBP、GIF、SVG、ICO 图片", "资源类型无效", "请选择要操作的资源", "只能操作上传目录内的资源", "资源路径无效", "上传目录无效":
		return message, ""
	default:
		return "操作未完成，请检查填写内容或稍后重试。", ""
	}
}
