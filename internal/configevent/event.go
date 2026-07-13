package configevent

import "context"

type Scope string

const (
	ScopeRuntimeSettings Scope = "runtime_settings"
	ScopeAccountStrategy Scope = "account_strategy"
	ScopeAccounts        Scope = "accounts"
)

type Subscription interface {
	Receive(context.Context) ([]byte, error)
	Close() error
}

type Bus interface {
	Publish(context.Context, string, []byte) error
	Subscribe(context.Context, string) (Subscription, error)
}

type Notifier interface {
	Notify(context.Context, Scope) error
}
