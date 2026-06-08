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
	defaultCheckInterval = 30 * time.Second
	defaultHealthTimeout = 2 * time.Second
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Config struct {
	Title         string        `yaml:"title"`
	Subtitle      string        `yaml:"subtitle"`
	CheckInterval time.Duration `yaml:"check_interval"`
	Groups        []Group       `yaml:"groups"`
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
		c.Title = "个人服务导航"
	}
	c.Subtitle = strings.TrimSpace(c.Subtitle)
	if c.CheckInterval == 0 {
		c.CheckInterval = defaultCheckInterval
	}
	if c.CheckInterval < 5*time.Second {
		return fmt.Errorf("配置错误: check_interval 不能小于 5s")
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
	if len(serviceIDs) == 0 {
		return fmt.Errorf("配置错误: 至少需要配置一个服务")
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
