package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"
	StatusUnhealthy HealthStatus = "unhealthy"
	StatusUnknown   HealthStatus = "unknown"
	StatusDisabled  HealthStatus = "disabled"
)

type ServiceStatus struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	GroupID   string       `json:"group_id"`
	GroupName string       `json:"group_name"`
	Type      string       `json:"type"`
	Status    HealthStatus `json:"status"`
	CheckedAt *time.Time   `json:"checked_at,omitempty"`
	LatencyMS int64        `json:"latency_ms,omitempty"`
	Error     string       `json:"error,omitempty"`
}

type StatusResponse struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Services    map[string]ServiceStatus `json:"services"`
}

type StatusCache struct {
	mu       sync.RWMutex
	services []Service
	statuses map[string]ServiceStatus
	client   *http.Client
}

func NewStatusCache(cfg *Config) *StatusCache {
	statuses := make(map[string]ServiceStatus)
	var services []Service
	for _, group := range cfg.Groups {
		for _, service := range group.Services {
			services = append(services, service)
			status := StatusUnknown
			if service.Health.Type == "disabled" {
				status = StatusDisabled
			}
			statuses[service.ID] = ServiceStatus{
				ID:        service.ID,
				Name:      service.Name,
				GroupID:   service.GroupID,
				GroupName: service.GroupName,
				Type:      service.Health.Type,
				Status:    status,
			}
		}
	}

	return &StatusCache{
		services: services,
		statuses: statuses,
		client: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *StatusCache) Snapshot() StatusResponse {
	c.mu.RLock()
	defer c.mu.RUnlock()

	services := make(map[string]ServiceStatus, len(c.statuses))
	for id, status := range c.statuses {
		services[id] = status
	}
	return StatusResponse{
		GeneratedAt: time.Now(),
		Services:    services,
	}
}

func (c *StatusCache) Start(ctx context.Context, interval time.Duration) {
	go func() {
		c.CheckAll(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.CheckAll(ctx)
			}
		}
	}()
}

func (c *StatusCache) CheckAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, service := range c.services {
		service := service
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.setStatus(checkService(ctx, c.client, service))
		}()
	}
	wg.Wait()
}

func (c *StatusCache) setStatus(status ServiceStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[status.ID] = status
}

func checkService(ctx context.Context, client *http.Client, service Service) ServiceStatus {
	result := ServiceStatus{
		ID:        service.ID,
		Name:      service.Name,
		GroupID:   service.GroupID,
		GroupName: service.GroupName,
		Type:      service.Health.Type,
		Status:    StatusUnknown,
	}
	if service.Health.Type == "disabled" {
		result.Status = StatusDisabled
		return result
	}

	start := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, service.Health.Timeout)
	defer cancel()

	var err error
	switch service.Health.Type {
	case "http":
		err = checkHTTP(checkCtx, client, service.Health)
	case "tcp":
		err = checkTCP(checkCtx, service.Health)
	default:
		err = fmt.Errorf("未知健康检查类型")
	}

	checkedAt := time.Now()
	result.CheckedAt = &checkedAt
	result.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = StatusUnhealthy
		result.Error = err.Error()
		return result
	}
	result.Status = StatusHealthy
	return result
}

func checkHTTP(ctx context.Context, client *http.Client, health HealthCheck) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, health.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("HTTP 检查超时")
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != health.ExpectStatus {
		return fmt.Errorf("HTTP 状态码 %d，预期 %d", resp.StatusCode, health.ExpectStatus)
	}
	return nil
}

func checkTCP(ctx context.Context, health HealthCheck) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", health.Address)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("TCP 检查超时")
		}
		return err
	}
	_ = conn.Close()
	return nil
}
