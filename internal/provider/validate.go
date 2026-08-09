package provider

import (
	"fmt"
	"sort"
)

// Validate 校验配置的结构完整性，供加载后调用。
// 校验项：
//   - providers 非空
//   - default_provider 存在（若指定）
//   - 每个 provider 有非空的 models、至少一个 key 来源
//   - 每个模型名非空、context_window 必须 >= 0（0 表示回退默认，不算错）
//   - thinking.efforts 中每个档位若配置必须在 low/high/max 白名单内
//
// 返回所有错误（多行），便于一次修完。
func (c Config) Validate() error {
	var errs []string

	if len(c.Providers) == 0 {
		errs = append(errs, "providers: no providers configured")
	} else {
		if c.DefaultProvider != "" {
			if _, ok := c.Providers[c.DefaultProvider]; !ok {
				errs = append(errs, fmt.Sprintf("default_provider: %q not found in providers", c.DefaultProvider))
			}
		}
		// 按名排序保证错误信息确定性。
		names := make([]string, 0, len(c.Providers))
		for name := range c.Providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			p := c.Providers[name]
			errs = append(errs, validateProvider(name, p)...)
		}
	}

	// approval.mode 合法值（与 approval 包 Modes 对齐；字面量避免 provider→approval
	// 循环依赖——approval → middleware → provider 已存在）。
	if c.Approval != nil && c.Approval.Mode != "" {
		switch c.Approval.Mode {
		case "readonly", "acceptedit", "bypass":
		default:
			errs = append(errs, fmt.Sprintf("approval.mode: %q invalid (want readonly, acceptedit or bypass)", c.Approval.Mode))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("config validation failed:\n  - %s", joinErrs(errs))
}

// validateProvider 校验单个 provider。
func validateProvider(name string, p ProviderConfig) []string {
	var errs []string
	prefix := "providers." + name

	if len(p.Models) == 0 {
		errs = append(errs, fmt.Sprintf("%s.models: no models configured", prefix))
	} else {
		modelNames := make([]string, 0, len(p.Models))
		for m := range p.Models {
			modelNames = append(modelNames, m)
		}
		sort.Strings(modelNames)
		for _, m := range modelNames {
			if m == "" {
				errs = append(errs, fmt.Sprintf("%s.models: empty model name", prefix))
				continue
			}
			if p.Models[m].ContextWindow < 0 {
				errs = append(errs, fmt.Sprintf("%s.models.%s.context_window: %d invalid (must be >= 0)", prefix, m, p.Models[m].ContextWindow))
			}
			if t := p.Models[m].Thinking; t != nil {
				for _, e := range t.Efforts {
					if e != EffortLow && e != EffortHigh && e != EffortMax {
						errs = append(errs, fmt.Sprintf("%s.models.%s.thinking.efforts: %q invalid (want low, high or max)", prefix, m, e))
					}
				}
			}
		}
	}

	if p.APIKey == "" && p.EnvKey == "" {
		errs = append(errs, fmt.Sprintf("%s: no API key (set api_key or env_key)", prefix))
	}

	return errs
}

func joinErrs(errs []string) string {
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += "\n  - "
		}
		out += e
	}
	return out
}
