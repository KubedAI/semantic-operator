package main

import (
	"log/slog"

	"github.com/KubedAI/semantic-operator/internal/cache"
	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/serving"
	"github.com/KubedAI/semantic-operator/internal/serving/auth"
	"github.com/KubedAI/semantic-operator/internal/serving/exchange"
)

// This file maps the config catalog onto the runtime option types. The mapping
// is pure so it can be unit-tested without constructing real clients.

func toLimits(q QueryConfig) serving.Limits {
	return serving.Limits{
		DefaultRowLimit:      q.DefaultRowLimit,
		MaxRowLimit:          q.MaxRowLimit,
		MaxMetrics:           q.MaxMetrics,
		MaxDimensions:        q.MaxDimensions,
		MaxFilters:           q.MaxFilters,
		MaxFilterValues:      q.MaxFilterValues,
		MaxResultBytes:       q.MaxResultBytes,
		MaxCacheEntryBytes:   q.MaxCacheEntryBytes,
		MaxRequestBytes:      q.MaxRequestBytes,
		MaxConcurrentQueries: q.MaxConcurrent,
	}
}

// toEngineConfig builds the engine connection config. MaxResultBytes is sourced
// from the query limits so the client and the service share one ceiling.
func toEngineConfig(e EngineConfig, q QueryConfig) dbclient.Config {
	return dbclient.Config{
		Host:                  e.Connection.Host,
		Port:                  e.Connection.Port,
		User:                  e.Connection.User,
		Password:              e.Connection.Password,
		QueryTimeout:          e.Connection.QueryTimeout,
		MaxResultBytes:        q.MaxResultBytes,
		TLSEnabled:            e.Connection.TLSEnabled,
		TLSInsecureSkipVerify: e.Connection.TLSInsecureSkipVerify,
	}
}

func toAuthOptions(a AuthConfig) auth.Options {
	return auth.Options{
		Mode:            auth.Mode(a.Mode),
		JWKSURL:         a.JWKSURL,
		Issuer:          a.Issuer,
		Audience:        a.Audience,
		PrincipalClaim:  a.PrincipalClaim,
		RoleClaim:       a.RoleClaim,
		GroupsClaim:     a.GroupsClaim,
		ClaimsToCopy:    a.ClaimsToCopy,
		EngineUserClaim: a.EngineUserClaim,
	}
}

func toCacheOptions(c CacheConfig) cache.Options {
	return cache.Options{
		Addr:      c.Addr,
		Password:  c.Password,
		DB:        c.DB,
		PlanTTL:   c.PlanTTL,
		ResultTTL: c.ResultTTL,
	}
}

func toExchangeOptions(e ExchangeConfig, log *slog.Logger) exchange.Options {
	return exchange.Options{
		TokenURL:          e.TokenURL,
		ClientID:          e.ClientID,
		ClientSecret:      e.ClientSecret,
		AllowInsecureHTTP: e.AllowInsecureHTTP,
		Logger:            log,
	}
}
