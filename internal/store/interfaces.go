package store

import (
	"context"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type AccountRepository interface {
	CreateAccount(context.Context, *domain.Account) error
	GetAccount(context.Context, string) (*domain.Account, error)
	ListAccounts(context.Context, AccountFilter) ([]domain.Account, error)
	CountAccounts(context.Context, AccountFilter) (int64, error)
	UpdateAccount(context.Context, *domain.Account) error
	UpdateAccounts(context.Context, []*domain.Account) error
	DeleteAccount(context.Context, string) error
	DeleteAccounts(context.Context, []string) error
}

type ModelRepository interface {
	CreateModel(context.Context, *domain.ModelSpec) error
	GetModel(context.Context, string) (*domain.ModelSpec, error)
	ListModels(context.Context, ModelFilter) ([]domain.ModelSpec, error)
	CountModels(context.Context, ModelFilter) (int64, error)
	UpdateModel(context.Context, *domain.ModelSpec) error
	DeleteModel(context.Context, string) error
}

type ProxyRepository interface {
	CreateProxy(context.Context, *domain.Proxy) error
	GetProxy(context.Context, string) (*domain.Proxy, error)
	ListProxies(context.Context, Pagination) ([]domain.Proxy, error)
	CountProxies(context.Context) (int64, error)
	UpdateProxy(context.Context, *domain.Proxy) error
	DeleteProxy(context.Context, string) error
}

type ClientKeyRepository interface {
	CreateClientKey(context.Context, *domain.ClientKey) error
	GetClientKey(context.Context, string) (*domain.ClientKey, error)
	GetClientKeyByDigest(context.Context, []byte) (*domain.ClientKey, error)
	ListClientKeys(context.Context, Pagination) ([]domain.ClientKey, error)
	CountClientKeys(context.Context) (int64, error)
	CountActiveClientKeys(context.Context, time.Time) (int64, error)
	UpdateClientKey(context.Context, *domain.ClientKey) error
	TouchClientKey(context.Context, string, time.Time) error
	DeleteClientKey(context.Context, string) error
}

type RequestLogRepository interface {
	CreateRequestLog(context.Context, *domain.RequestLog) error
	GetRequestLog(context.Context, string) (*domain.RequestLog, error)
	ListRequestLogs(context.Context, RequestLogFilter) ([]domain.RequestLog, error)
	CountRequestLogs(context.Context, RequestLogFilter) (int64, error)
	GetRequestLogStats(context.Context, time.Time, time.Time) (*RequestLogStats, error)
	UpdateRequestLog(context.Context, *domain.RequestLog) error
	DeleteRequestLog(context.Context, string) error
	DeleteRequestLogsBefore(context.Context, time.Time) (int64, error)
}

type AuditLogRepository interface {
	CreateAuditLog(context.Context, *domain.AuditLog) error
	ListAuditLogs(context.Context, AuditLogFilter) ([]domain.AuditLog, error)
	CountAuditLogs(context.Context, AuditLogFilter) (int64, error)
	DeleteAuditLogsBefore(context.Context, time.Time) (int64, error)
}

type AdminRepository interface {
	CountAdmins(context.Context) (int64, error)
	CreateAdmin(context.Context, *AdminRecord) error
	GetAdminByID(context.Context, string) (*AdminRecord, error)
	GetAdminByEmail(context.Context, string) (*AdminRecord, error)
	UpdateAdminEmail(context.Context, string, string) error
	UpdateAdminPassword(context.Context, string, string) error
	SetPendingTOTP(context.Context, string, []byte, time.Time) error
	EnableTOTP(context.Context, string, []byte) error
	DisableTOTP(context.Context, string) error
	RecordAdminLogin(context.Context, string, time.Time) error
	CreateAdminSession(context.Context, *AdminSession) error
	GetAdminSessionByDigest(context.Context, []byte) (*AdminSession, error)
	TouchAdminSession(context.Context, string, time.Time) error
	DeleteAdminSessionByDigest(context.Context, []byte) error
	DeleteAdminSessionsForAdmin(context.Context, string) error
	DeleteExpiredAdminSessions(context.Context, time.Time) (int64, error)
}

var (
	_ AccountRepository    = (*Postgres)(nil)
	_ ModelRepository      = (*Postgres)(nil)
	_ ProxyRepository      = (*Postgres)(nil)
	_ ClientKeyRepository  = (*Postgres)(nil)
	_ RequestLogRepository = (*Postgres)(nil)
	_ AuditLogRepository   = (*Postgres)(nil)
	_ AdminRepository      = (*Postgres)(nil)
)
