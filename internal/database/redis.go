package database

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/config"
)

// NewRedis creates a new Redis client
func NewRedis(cfg *config.RedisConfig) (*redis.Client, error) {
	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
	}

	if cfg.TLS {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Allow connecting to a Redis that presents a self-signed / private-CA
			// certificate (common for managed Redis on an internal network, e.g.
			// Coolify). Opt-in only — safe on a trusted private network.
			InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // gated by explicit config
		}
	}

	client := redis.NewClient(opts)

	// Test connection
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return client, nil
}
