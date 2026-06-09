package server

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	defaultCheckInterval     = 30 * time.Second
	defaultHealthTimeout     = 2 * time.Second
	defaultBackgroundOverlay = "medium"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Config struct {
	Title         string        `yaml:"title"`
	Subtitle      string        `yaml:"subtitle"`
	Auth          AuthConfig    `yaml:"auth"`
	Assets        AssetsConfig  `yaml:"assets"`
	Appearance    Appearance    `yaml:"appearance"`
	CheckInterval time.Duration `yaml:"check_interval"`
	Groups        []Group       `yaml:"groups"`
}

type Appearance struct {
	BackgroundColor   string `yaml:"background_color" json:"background_color"`
	BackgroundImage   string `yaml:"background_image" json:"background_image"`
	BackgroundOverlay string `yaml:"background_overlay" json:"background_overlay"`
}

type AssetsConfig struct {
	UploadsDir       string `yaml:"uploads_dir"`
	UploadsURLPrefix string `yaml:"uploads_url_prefix"`
	IconCacheDir     string `yaml:"icon_cache_dir"`
}

type AuthConfig struct {
	Enabled       bool          `yaml:"enabled"`
	Username      string        `yaml:"username"`
	Password      string        `yaml:"password"`
	SessionSecret string        `yaml:"session_secret"`
	SessionTTL    time.Duration `yaml:"session_ttl"`
}

type Group struct {
	ID       string    `yaml:"id"`
	Name     string    `yaml:"name"`
	Services []Service `yaml:"services"`
}

type Service struct {
	ID          string      `yaml:"id"`
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	IconText    string      `yaml:"icon_text"`
	Icon        string      `yaml:"icon"`
	InternalURL string      `yaml:"internal_url"`
	ExternalURL string      `yaml:"external_url"`
	Tags        []string    `yaml:"tags"`
	Notes       string      `yaml:"notes"`
	Health      HealthCheck `yaml:"health"`
	GroupID     string      `yaml:"-"`
	GroupName   string      `yaml:"-"`
}

func (s Service) DisplayIconText() string {
	if s.IconText != "" {
		return s.IconText
	}
	if s.Name == "" {
		return "?"
	}
	r, _ := utf8.DecodeRuneInString(s.Name)
	if r == utf8.RuneError {
		return "?"
	}
	return strings.ToUpper(string(r))
}

func (s Service) IconIsImage() bool {
	return strings.HasPrefix(s.Icon, "http://") || strings.HasPrefix(s.Icon, "https://") || strings.HasPrefix(s.Icon, "/")
}

func (s Service) IconIsOnline() bool {
	return s.Icon != "" && strings.Contains(s.Icon, ":") && !s.IconIsImage()
}

func (s Service) IconImageSrc() string {
	if !s.IconIsOnline() {
		return s.Icon
	}
	collection, name, ok := strings.Cut(s.Icon, ":")
	if !ok || collection == "" || name == "" {
		return ""
	}
	return "/.iconify/" + url.PathEscape(collection) + "/" + url.PathEscape(name) + ".svg"
}

func (s Service) DefaultURL() string {
	if s.ExternalURL != "" {
		return s.ExternalURL
	}
	return s.InternalURL
}

type HealthCheck struct {
	Type         string        `yaml:"type"`
	URL          string        `yaml:"url"`
	Address      string        `yaml:"address"`
	ExpectStatus int           `yaml:"expect_status"`
	Timeout      time.Duration `yaml:"timeout"`
}

func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	defer f.Close()

	var cfg Config
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) NormalizeAndValidate() error {
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		c.Title = "Home"
	}
	c.Subtitle = strings.TrimSpace(c.Subtitle)
	if c.CheckInterval == 0 {
		c.CheckInterval = defaultCheckInterval
	}
	if c.CheckInterval < 5*time.Second {
		return fmt.Errorf("配置错误: check_interval 不能小于 5s")
	}
	if err := normalizeAuth(&c.Auth); err != nil {
		return err
	}
	if err := normalizeAssets(&c.Assets); err != nil {
		return err
	}
	if err := normalizeAppearance(&c.Appearance); err != nil {
		return err
	}
	if len(c.Groups) == 0 {
		return fmt.Errorf("配置错误: groups 不能为空")
	}

	groupIDs := make(map[string]struct{})
	serviceIDs := make(map[string]struct{})
	for gi := range c.Groups {
		group := &c.Groups[gi]
		group.ID = strings.TrimSpace(group.ID)
		group.Name = strings.TrimSpace(group.Name)
		if err := validateID("group id", group.ID); err != nil {
			return fmt.Errorf("配置错误: groups[%d]: %w", gi, err)
		}
		if group.Name == "" {
			return fmt.Errorf("配置错误: groups[%d].name 不能为空", gi)
		}
		if _, ok := groupIDs[group.ID]; ok {
			return fmt.Errorf("配置错误: group id %q 重复", group.ID)
		}
		groupIDs[group.ID] = struct{}{}

		for si := range group.Services {
			service := &group.Services[si]
			if err := normalizeService(service, group.ID, group.Name); err != nil {
				return fmt.Errorf("配置错误: groups[%d].services[%d]: %w", gi, si, err)
			}
			if _, ok := serviceIDs[service.ID]; ok {
				return fmt.Errorf("配置错误: service id %q 重复", service.ID)
			}
			serviceIDs[service.ID] = struct{}{}
		}
	}
	return nil
}

func normalizeAppearance(appearance *Appearance) error {
	appearance.BackgroundColor = strings.TrimSpace(appearance.BackgroundColor)
	appearance.BackgroundImage = strings.TrimSpace(appearance.BackgroundImage)
	appearance.BackgroundOverlay = strings.TrimSpace(appearance.BackgroundOverlay)
	if appearance.BackgroundColor == "" {
		appearance.BackgroundColor = "#000000"
	}
	if appearance.BackgroundOverlay == "" {
		appearance.BackgroundOverlay = defaultBackgroundOverlay
	}
	if !validHexColor(appearance.BackgroundColor) {
		return fmt.Errorf("配置错误: appearance.background_color 必须是 #RGB 或 #RRGGBB")
	}
	if !validBackgroundOverlay(appearance.BackgroundOverlay) {
		return fmt.Errorf("配置错误: appearance.background_overlay 必须是 low、medium 或 high")
	}
	if appearance.BackgroundImage == "" {
		return nil
	}
	if strings.HasPrefix(appearance.BackgroundImage, "/uploads/") {
		return nil
	}
	if err := validateWebURL("appearance.background_image", appearance.BackgroundImage); err != nil {
		return err
	}
	return nil
}

func validBackgroundOverlay(value string) bool {
	switch value {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func validHexColor(value string) bool {
	if len(value) != 4 && len(value) != 7 {
		return false
	}
	if value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func normalizeAssets(assets *AssetsConfig) error {
	assets.UploadsDir = strings.TrimSpace(assets.UploadsDir)
	assets.UploadsURLPrefix = strings.TrimSpace(assets.UploadsURLPrefix)
	assets.IconCacheDir = strings.TrimSpace(assets.IconCacheDir)
	if assets.UploadsURLPrefix == "" {
		assets.UploadsURLPrefix = "/uploads/"
	}
	if assets.UploadsDir == "" && assets.IconCacheDir == "" {
		return nil
	}
	if assets.UploadsDir != "" && (!strings.HasPrefix(assets.UploadsURLPrefix, "/") || !strings.HasSuffix(assets.UploadsURLPrefix, "/")) {
		return fmt.Errorf("配置错误: assets.uploads_url_prefix 必须以 / 开头并以 / 结尾")
	}
	if assets.UploadsDir != "" {
		info, err := os.Stat(assets.UploadsDir)
		if err != nil {
			return fmt.Errorf("配置错误: assets.uploads_dir 不可访问: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("配置错误: assets.uploads_dir 必须是目录")
		}
	}
	if assets.IconCacheDir != "" {
		if err := os.MkdirAll(assets.IconCacheDir, 0700); err != nil {
			return fmt.Errorf("配置错误: assets.icon_cache_dir 不可创建: %w", err)
		}
		info, err := os.Stat(assets.IconCacheDir)
		if err != nil {
			return fmt.Errorf("配置错误: assets.icon_cache_dir 不可访问: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("配置错误: assets.icon_cache_dir 必须是目录")
		}
	}
	return nil
}

func normalizeAuth(auth *AuthConfig) error {
	auth.Username = strings.TrimSpace(auth.Username)
	auth.Password = strings.TrimSpace(auth.Password)
	auth.SessionSecret = strings.TrimSpace(auth.SessionSecret)
	if auth.SessionTTL == 0 {
		auth.SessionTTL = 24 * time.Hour
	}
	if auth.SessionTTL < time.Minute {
		return fmt.Errorf("配置错误: auth.session_ttl 不能小于 1m")
	}
	if !auth.Enabled {
		return nil
	}
	if auth.Username == "" {
		return fmt.Errorf("配置错误: auth.username 不能为空")
	}
	if auth.Password == "" {
		return fmt.Errorf("配置错误: auth.password 不能为空")
	}
	if auth.Password == defaultAuthPassword {
		return fmt.Errorf("配置错误: auth.password 不能使用示例默认值")
	}
	if len(auth.SessionSecret) < 32 {
		return fmt.Errorf("配置错误: auth.session_secret 至少需要 32 个字符")
	}
	if auth.SessionSecret == defaultAuthSessionSecret {
		return fmt.Errorf("配置错误: auth.session_secret 不能使用示例默认值")
	}
	return nil
}

func normalizeService(service *Service, groupID, groupName string) error {
	service.ID = strings.TrimSpace(service.ID)
	service.Name = strings.TrimSpace(service.Name)
	service.Description = strings.TrimSpace(service.Description)
	service.IconText = strings.TrimSpace(service.IconText)
	service.Icon = strings.TrimSpace(service.Icon)
	service.InternalURL = strings.TrimSpace(service.InternalURL)
	service.ExternalURL = strings.TrimSpace(service.ExternalURL)
	service.Notes = strings.TrimSpace(service.Notes)
	service.GroupID = groupID
	service.GroupName = groupName

	if err := validateID("service id", service.ID); err != nil {
		return err
	}
	if service.Name == "" {
		return fmt.Errorf("name 不能为空")
	}
	if service.InternalURL == "" && service.ExternalURL == "" {
		return fmt.Errorf("internal_url 和 external_url 至少需要配置一个")
	}
	if service.InternalURL != "" {
		if err := validateWebURL("internal_url", service.InternalURL); err != nil {
			return err
		}
	}
	if service.ExternalURL != "" {
		if err := validateWebURL("external_url", service.ExternalURL); err != nil {
			return err
		}
	}
	for i := range service.Tags {
		service.Tags[i] = strings.TrimSpace(service.Tags[i])
		if service.Tags[i] == "" {
			return fmt.Errorf("tags[%d] 不能为空", i)
		}
	}
	return normalizeHealth(&service.Health)
}

func normalizeHealth(health *HealthCheck) error {
	health.Type = strings.ToLower(strings.TrimSpace(health.Type))
	health.URL = strings.TrimSpace(health.URL)
	health.Address = strings.TrimSpace(health.Address)
	if health.Timeout == 0 {
		health.Timeout = defaultHealthTimeout
	}
	if health.Timeout <= 0 {
		return fmt.Errorf("health.timeout 必须大于 0")
	}

	switch health.Type {
	case "disabled":
		return nil
	case "http":
		if health.URL == "" {
			return fmt.Errorf("health.url 不能为空")
		}
		if err := validateWebURL("health.url", health.URL); err != nil {
			return err
		}
		if health.ExpectStatus == 0 {
			health.ExpectStatus = 200
		}
		if health.ExpectStatus < 100 || health.ExpectStatus > 599 {
			return fmt.Errorf("health.expect_status 必须是 100-599")
		}
		return nil
	case "tcp":
		if health.Address == "" {
			return fmt.Errorf("health.address 不能为空")
		}
		if !strings.Contains(health.Address, ":") {
			return fmt.Errorf("health.address 必须是 host:port")
		}
		return nil
	default:
		return fmt.Errorf("health.type 仅支持 disabled/http/tcp")
	}
}

func validateID(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s 不能为空", label)
	}
	if !idPattern.MatchString(value) {
		return fmt.Errorf("%s %q 只能使用小写字母、数字和连字符，并且必须以字母或数字开头", label, value)
	}
	return nil
}

func validateWebURL(label, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s 必须是完整 http/https URL", label)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s 仅支持 http/https", label)
	}
	return nil
}
