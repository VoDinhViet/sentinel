package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/config"
)

// ConfigSync keeps a managed agent's configuration in step with the server.
// The effective config is the agent's local-only keys (server_name, store,
// central, restore_dir) plus the managed YAML from the server.
type ConfigSync struct {
	Client     *Client
	LocalRaw   []byte // the agent's own config file
	CachePath  string // last applied managed config, for starting offline
	Supervisor *Supervisor
}

// build merges and fully parses (with env expansion) a managed config.
func (c *ConfigSync) build(doc api.ConfigDoc) (*config.Config, error) {
	merged, err := config.MergeManaged(c.LocalRaw, []byte(doc.YAML))
	if err != nil {
		return nil, err
	}
	return config.Parse(merged)
}

func (c *ConfigSync) fetch(ctx context.Context, have int64) (api.ConfigDoc, error) {
	var doc api.ConfigDoc
	err := c.Client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/agent/config?version=%d", have), nil, &doc)
	return doc, err
}

func (c *ConfigSync) report(ctx context.Context, st api.ConfigStatus) {
	if err := c.Client.Do(ctx, http.MethodPost, "/api/agent/config/status", st, nil); err != nil {
		slog.Warn("report config status", "error", err)
	}
}

func (c *ConfigSync) readCache() (api.ConfigDoc, bool) {
	var doc api.ConfigDoc
	raw, err := os.ReadFile(c.CachePath)
	if err != nil || json.Unmarshal(raw, &doc) != nil || doc.Version == 0 {
		return doc, false
	}
	return doc, true
}

func (c *ConfigSync) writeCache(doc api.ConfigDoc) {
	raw, _ := json.Marshal(doc)
	tmp := c.CachePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		slog.Warn("write config cache", "error", err)
		return
	}
	if err := os.Rename(tmp, c.CachePath); err != nil {
		slog.Warn("write config cache", "error", err)
	}
}

// Initial returns the config to start with. It prefers the server's version,
// uploads the local file if the server has none, and falls back to the last
// cached version (or the local file) when the server is unreachable or its
// config does not work here.
func (c *ConfigSync) Initial(ctx context.Context) (*config.Config, int64, error) {
	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	doc, err := c.fetch(fctx, 0)
	if IsStatus(err, http.StatusNotFound) {
		stripped, serr := config.StripLocal(c.LocalRaw)
		if serr != nil {
			return nil, 0, serr
		}
		slog.Info("server has no config for this agent; uploading the local one")
		if err = c.Client.Do(fctx, http.MethodPut, "/api/agent/config", api.ConfigDoc{YAML: string(stripped)}, nil); err == nil || IsStatus(err, http.StatusConflict) {
			doc, err = c.fetch(fctx, 0)
		}
	}
	if err == nil {
		cfg, berr := c.build(doc)
		if berr == nil {
			c.writeCache(doc)
			c.report(ctx, api.ConfigStatus{Version: doc.Version, Applied: true})
			return cfg, doc.Version, nil
		}
		slog.Error("managed config from server is not usable here", "version", doc.Version, "error", berr)
		c.report(ctx, api.ConfigStatus{Version: doc.Version, Error: berr.Error()})
	} else {
		slog.Warn("cannot fetch managed config; starting from cache", "error", err)
	}

	if cached, ok := c.readCache(); ok {
		if cfg, err := c.build(cached); err == nil {
			return cfg, cached.Version, nil
		}
	}
	cfg, err := config.Parse(c.LocalRaw)
	if err != nil {
		return nil, 0, fmt.Errorf("no usable config (server, cache or local file): %w", err)
	}
	return cfg, 0, nil
}

// Run polls for new versions and hands them to the supervisor.
func (c *ConfigSync) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var rejected int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		_, have := c.Supervisor.Current()
		doc, err := c.fetch(ctx, have)
		if errors.Is(err, ErrNotModified) || IsStatus(err, http.StatusNotFound) {
			continue
		}
		if err != nil {
			slog.Debug("poll config failed", "error", err)
			continue
		}
		if doc.Version == rejected {
			continue // already reported as unusable
		}
		cfg, err := c.build(doc)
		if err != nil {
			slog.Error("new managed config rejected; keeping the current one", "version", doc.Version, "error", err)
			rejected = doc.Version
			c.report(ctx, api.ConfigStatus{Version: doc.Version, Error: err.Error()})
			continue
		}
		c.writeCache(doc)
		v := doc.Version
		c.Supervisor.Reload(cfg, v, func() {
			c.report(context.WithoutCancel(ctx), api.ConfigStatus{Version: v, Applied: true})
		})
	}
}
