package audit

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
)

type RuntimeConfig struct {
	ModelConcurrency   int `json:"model_concurrency"`
	RequestConcurrency int `json:"request_concurrency"`
	RequestBodyMiB     int `json:"request_body_mib"`
	TrialConcurrency   int `json:"trial_concurrency"`
	MaxImages          int `json:"max_images"`
	IngressRPM         int `json:"ingress_rpm"`
	IngressIPRPM       int `json:"ingress_ip_rpm"`
}

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{ModelConcurrency: 16, RequestConcurrency: 32, RequestBodyMiB: 128, TrialConcurrency: 2, MaxImages: 16, IngressRPM: 6000, IngressIPRPM: 600}
}
func RuntimeConfigFromEnv() (RuntimeConfig, error) {
	c := DefaultRuntimeConfig()
	for name, target := range map[string]*int{
		"AUDIT_MODEL_CONCURRENCY": &c.ModelConcurrency, "AUDIT_REQUEST_CONCURRENCY": &c.RequestConcurrency,
		"AUDIT_REQUEST_BODY_MIB": &c.RequestBodyMiB, "AUDIT_TRIAL_CONCURRENCY": &c.TrialConcurrency, "AUDIT_MAX_IMAGES": &c.MaxImages,
		"AUDIT_INGRESS_RPM": &c.IngressRPM, "AUDIT_INGRESS_IP_RPM": &c.IngressIPRPM,
	} {
		if raw := os.Getenv(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				return c, fmt.Errorf("%s must be an integer", name)
			}
			*target = value
		}
	}
	if os.Getenv("AUDIT_TRIAL_CONCURRENCY") == "" {
		c.TrialConcurrency = min(c.TrialConcurrency, c.ModelConcurrency)
	}
	return c, c.Validate()
}
func (c RuntimeConfig) Validate() error {
	if c.IngressRPM < 1 || c.IngressRPM > 1000000 || c.IngressIPRPM < 1 || c.IngressIPRPM > c.IngressRPM {
		return errors.New("invalid ingress limits: global RPM 1-1000000, per-IP RPM 1-global RPM")
	}
	if c.ModelConcurrency < 1 || c.ModelConcurrency > 256 || c.RequestConcurrency < 1 || c.RequestConcurrency > 1024 || c.RequestBodyMiB < 32 || c.RequestBodyMiB > 4096 || c.TrialConcurrency < 1 || c.TrialConcurrency > c.ModelConcurrency || c.MaxImages < 1 || c.MaxImages > 256 {
		return errors.New("invalid audit limits: model concurrency 1-256, request concurrency 1-1024, body budget 32-4096 MiB, trial concurrency 1-model concurrency, images 1-256")
	}
	return nil
}

type requestAdmission struct {
	mu     sync.Mutex
	active int
	bytes  int64
	config RuntimeConfig
}

func (a *requestAdmission) acquire(length, limit int64) (func(), error) {
	if length > limit {
		return nil, problem(413, "body_too_large", "请求体超过该接口大小限制")
	}
	if length < 0 {
		length = limit
	}
	a.mu.Lock()
	if a.active >= a.config.RequestConcurrency || a.bytes+length > int64(a.config.RequestBodyMiB)<<20 {
		a.mu.Unlock()
		return nil, problem(503, "request_capacity_exceeded", "在途请求或请求体预算已满，请稍后重试")
	}
	a.active++
	a.bytes += length
	a.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { a.mu.Lock(); a.active--; a.bytes -= length; a.mu.Unlock() }) }, nil
}
