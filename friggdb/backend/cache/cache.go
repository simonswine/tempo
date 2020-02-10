package cache

import (
	"fmt"
	"os"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"

	"github.com/google/uuid"
	"github.com/grafana/frigg/friggdb/backend"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type missFunc func(blockID uuid.UUID, tenantID string) ([]byte, error)

const (
	typeBloom = "bloom"
	typeIndex = "index"
)

var (
	metricDiskCacheMiss = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "friggdb",
		Name:      "disk_cache_miss_total",
		Help:      "Total number of times the disk cache missed.",
	}, []string{"type"})
	metricDiskCache = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "friggdb",
		Name:      "disk_cache_total",
		Help:      "Total number of times there were errors checking the disk cache.",
	}, []string{"type", "status"})
	metricDiskCacheClean = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "friggdb",
		Name:      "disk_cache_clean_total",
		Help:      "Total number of times a disk clean has occurred.",
	}, []string{"status"})
)

type reader struct {
	cfg  *Config
	next backend.Reader

	logger log.Logger
	stopCh chan struct{}
}

func New(next backend.Reader, cfg *Config, logger log.Logger) (backend.Reader, error) {
	// cleanup disk cache dir
	err := os.RemoveAll(cfg.Path)
	if err != nil {
		return nil, err
	}
	err = os.MkdirAll(cfg.Path, os.ModePerm)
	if err != nil {
		return nil, err
	}

	if cfg.DiskPruneCount == 0 {
		return nil, fmt.Errorf("must specify disk prune count")
	}

	if cfg.DiskCleanRate == 0 {
		return nil, fmt.Errorf("must specify a clean rate")
	}

	if cfg.MaxDiskMBs == 0 {
		return nil, fmt.Errorf("must specify a maximum number of MBs to save")
	}

	r := &reader{
		cfg:    cfg,
		next:   next,
		stopCh: make(chan struct{}, 0),
		logger: logger,
	}

	go r.startJanitor()

	return r, nil
}

func (r *reader) Tenants() ([]string, error) {
	return r.next.Tenants()
}

func (r *reader) Blocklist(tenantID string) ([][]byte, error) {
	return r.next.Blocklist(tenantID)
}

// jpe: how to force cache all blooms at the start
func (r *reader) Bloom(blockID uuid.UUID, tenantID string) ([]byte, error) {
	b, skippableErr, err := r.readOrCacheKeyToDisk(blockID, tenantID, typeBloom, r.next.Bloom)

	if skippableErr != nil {
		metricDiskCache.WithLabelValues(typeBloom, "error").Inc()
		level.Error(r.logger).Log("err", skippableErr)
	} else {
		metricDiskCache.WithLabelValues(typeBloom, "success").Inc()
	}

	return b, err
}

func (r *reader) Index(blockID uuid.UUID, tenantID string) ([]byte, error) {
	b, skippableErr, err := r.readOrCacheKeyToDisk(blockID, tenantID, typeIndex, r.next.Index)

	if skippableErr != nil {
		metricDiskCache.WithLabelValues(typeIndex, "error").Inc()
		level.Error(r.logger).Log("err", skippableErr)
	} else {
		metricDiskCache.WithLabelValues(typeIndex, "success").Inc()
	}

	return b, err
}

func (r *reader) Object(blockID uuid.UUID, tenantID string, start uint64, length uint32) ([]byte, error) {
	// not attempting to cache these...yet...
	return r.next.Object(blockID, tenantID, start, length)
}

func (r *reader) Shutdown() {
	r.stopCh <- struct{}{}
}

func key(blockID uuid.UUID, tenantID string, t string) string {
	return blockID.String() + ":" + tenantID + ":" + t
}