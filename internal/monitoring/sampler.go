package monitoring

import (
	"context"
	"errors"
)

var ErrSamplerDependencyRequired = errors.New("业务监控采样器依赖不能为空")

type metricUpdater interface {
	Update(Snapshot)
}

// Sampler 周期读取数据库并更新内存指标。
type Sampler struct {
	source  Source
	metrics metricUpdater
}

// NewSampler 创建业务监控采样器。
func NewSampler(source Source, metrics metricUpdater) (*Sampler, error) {
	if source == nil || metrics == nil {
		return nil, ErrSamplerDependencyRequired
	}
	return &Sampler{source: source, metrics: metrics}, nil
}

// RunOnce 执行一次采样；成功后等待配置的刷新周期。
func (sampler *Sampler) RunOnce(ctx context.Context) (bool, error) {
	snapshot, err := sampler.source.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	sampler.metrics.Update(snapshot)
	return false, nil
}
