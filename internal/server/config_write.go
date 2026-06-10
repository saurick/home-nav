package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func SaveConfig(path string, cfg *Config) error {
	copyCfg := *cfg
	if err := copyCfg.NormalizeAndValidate(); err != nil {
		return err
	}

	content := []byte(renderConfigYAML(&copyCfg))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0600); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if writeErr := os.WriteFile(path, content, 0600); writeErr != nil {
			return fmt.Errorf("替换配置失败: %w；直接写入也失败: %w", err, writeErr)
		}
	}
	return nil
}

func renderConfigYAML(cfg *Config) string {
	var b strings.Builder
	line(&b, 0, "title: %s", yamlString(cfg.Title))
	line(&b, 0, "subtitle: %s", yamlString(cfg.Subtitle))
	line(&b, 0, "check_interval: %s", yamlString(durationString(cfg.CheckInterval)))
	if cfg.Auth.Enabled || cfg.Auth.Username != "" || cfg.Auth.Password != "" || cfg.Auth.SessionSecret != "" {
		b.WriteString("\n")
		line(&b, 0, "auth:")
		line(&b, 1, "enabled: %t", cfg.Auth.Enabled)
		line(&b, 1, "username: %s", yamlString(cfg.Auth.Username))
		line(&b, 1, "password: %s", yamlString(cfg.Auth.Password))
		line(&b, 1, "session_secret: %s", yamlString(cfg.Auth.SessionSecret))
	}
	if cfg.Assets.UploadsDir != "" || cfg.Assets.IconCacheDir != "" {
		b.WriteString("\n")
		line(&b, 0, "assets:")
		if cfg.Assets.UploadsDir != "" {
			line(&b, 1, "uploads_dir: %s", yamlString(cfg.Assets.UploadsDir))
			line(&b, 1, "uploads_url_prefix: %s", yamlString(cfg.Assets.UploadsURLPrefix))
		}
		if cfg.Assets.IconCacheDir != "" {
			line(&b, 1, "icon_cache_dir: %s", yamlString(cfg.Assets.IconCacheDir))
		}
	}
	b.WriteString("\n")
	line(&b, 0, "appearance:")
	line(&b, 1, "background_color: %s", yamlString(cfg.Appearance.BackgroundColor))
	line(&b, 1, "background_image: %s", yamlString(cfg.Appearance.BackgroundImage))
	line(&b, 1, "background_overlay: %s", yamlString(cfg.Appearance.BackgroundOverlay))
	b.WriteString("\n")
	line(&b, 0, "groups:")
	for _, group := range cfg.Groups {
		line(&b, 1, "- id: %s", group.ID)
		line(&b, 2, "name: %s", yamlString(group.Name))
		line(&b, 2, "services:")
		for _, service := range group.Services {
			line(&b, 3, "- id: %s", service.ID)
			line(&b, 4, "name: %s", yamlString(service.Name))
			line(&b, 4, "description: %s", yamlString(service.Description))
			line(&b, 4, "icon_text: %s", yamlString(service.IconText))
			line(&b, 4, "icon: %s", yamlString(service.Icon))
			line(&b, 4, "internal_url: %s", yamlString(service.InternalURL))
			line(&b, 4, "external_url: %s", yamlString(service.ExternalURL))
			if len(service.Tags) == 0 {
				line(&b, 4, "tags: []")
			} else {
				line(&b, 4, "tags:")
				for _, tag := range service.Tags {
					line(&b, 5, "- %s", yamlString(tag))
				}
			}
			line(&b, 4, "notes: %s", yamlString(service.Notes))
			line(&b, 4, "health:")
			line(&b, 5, "type: %s", service.Health.Type)
			if service.Health.URL != "" {
				line(&b, 5, "url: %s", yamlString(service.Health.URL))
			}
			if service.Health.Address != "" {
				line(&b, 5, "address: %s", yamlString(service.Health.Address))
			}
			if service.Health.ExpectStatus != 0 {
				line(&b, 5, "expect_status: %d", service.Health.ExpectStatus)
			}
			line(&b, 5, "timeout: %s", yamlString(durationString(service.Health.Timeout)))
		}
	}
	return b.String()
}

func line(b *strings.Builder, indent int, format string, args ...any) {
	b.WriteString(strings.Repeat("  ", indent))
	b.WriteString(fmt.Sprintf(format, args...))
	b.WriteByte('\n')
}

func yamlString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func durationString(value time.Duration) string {
	if value == 0 {
		return "0s"
	}
	return value.String()
}
