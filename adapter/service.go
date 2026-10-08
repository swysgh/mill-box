package adapter

import (
	"context"

	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"
)

type Service interface {
	Lifecycle
	Type() string
	Tag() string
}

type ServiceRegistry interface {
	option.ServiceOptionsRegistry
	Create(ctx context.Context, logger log.ContextLogger, tag string, serviceType string, options any) (Service, error)
}

type ServiceManager interface {
	Lifecycle
	Services() []Service
	Get(tag string) (Service, bool)
	Create(ctx context.Context, logger log.ContextLogger, tag string, serviceType string, options any) error
}
