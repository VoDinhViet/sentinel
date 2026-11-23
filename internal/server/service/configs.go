package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sergi/go-diff/diffmatchpatch"

	"sentinel/internal/api"
	"sentinel/internal/config"
)

const maxConfigBytes = 256 << 10

type ConfigVersion struct {
	Version   int64
	YAML      string
	Comment   string
	CreatedAt time.Time
	// Author is who saved the version: a username, "agent" for an import, "" for old rows.
	Author string
}

// ValidateConfig checks a managed config without expanding ${VAR}
// references, whose values only exist on the agent.
func ValidateConfig(yamlText string) error {
	if len(yamlText) > maxConfigBytes {
		return Invalid("config is larger than %d KiB", maxConfigBytes>>10)
	}
	if err := config.CheckManaged([]byte(yamlText)); err != nil {
		return Invalid("%v", err)
	}
	if _, err := config.ParseWith([]byte(yamlText), config.ParseOptions{ExpandEnv: false}); err != nil {
		return Invalid("%v", err)
	}
	return nil
}

// SaveConfig validates and stores a new config version for the agent.
func (s *Service) SaveConfig(ctx context.Context, agentID int64, yamlText, comment string) (int64, error) {
	if err := ValidateConfig(yamlText); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ?`, agentID).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, ErrNotFound
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM configs WHERE agent_id = ?`, agentID).Scan(&version); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO configs (agent_id, version, yaml, comment, created_at, author) VALUES (?, ?, ?, ?, ?, ?)`,
		agentID, version, yamlText, comment, s.now().Unix(), ActorFrom(ctx).Username); err != nil {
		return 0, err
	}
	return version, tx.Commit()
}

// ImportConfig stores the agent's own config as version 1, but only if the
// server has none yet (first connection of a managed agent).
func (s *Service) ImportConfig(ctx context.Context, agentID int64, yamlText string) (bool, error) {
	if _, ok, err := s.CurrentConfig(ctx, agentID); err != nil || ok {
		return false, err
	}
	_, err := s.SaveConfig(WithActor(ctx, Actor{Username: "agent"}), agentID, yamlText, "imported from agent")
	return err == nil, err
}

// CurrentConfig returns the newest version; ok is false if none exists.
func (s *Service) CurrentConfig(ctx context.Context, agentID int64) (doc api.ConfigDoc, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT version, yaml FROM configs WHERE agent_id = ?
		ORDER BY version DESC LIMIT 1`, agentID).Scan(&doc.Version, &doc.YAML)
	if errors.Is(err, sql.ErrNoRows) {
		return doc, false, nil
	}
	return doc, err == nil, err
}

func (s *Service) ConfigVersion(ctx context.Context, agentID, version int64) (ConfigVersion, error) {
	var v ConfigVersion
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT version, yaml, comment, created_at, author FROM configs
		WHERE agent_id = ? AND version = ?`, agentID, version).Scan(&v.Version, &v.YAML, &v.Comment, &created, &v.Author)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	v.CreatedAt = time.Unix(created, 0)
	return v, err
}

// ConfigHistory lists versions newest first (without YAML bodies).
func (s *Service) ConfigHistory(ctx context.Context, agentID int64, limit int) ([]ConfigVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version, comment, created_at, author FROM configs WHERE agent_id = ?
		ORDER BY version DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigVersion
	for rows.Next() {
		var v ConfigVersion
		var created int64
		if err := rows.Scan(&v.Version, &v.Comment, &created, &v.Author); err != nil {
			return nil, err
		}
		v.CreatedAt = time.Unix(created, 0)
		out = append(out, v)
	}
	return out, rows.Err()
}

// Rollback saves an old version's content as a new version.
func (s *Service) Rollback(ctx context.Context, agentID, version int64) (int64, error) {
	old, err := s.ConfigVersion(ctx, agentID, version)
	if err != nil {
		return 0, err
	}
	return s.SaveConfig(ctx, agentID, old.YAML, fmt.Sprintf("rollback to v%d", version))
}

// SetConfigStatus records whether the agent applied a version.
func (s *Service) SetConfigStatus(ctx context.Context, agentID int64, st api.ConfigStatus) error {
	errText := st.Error
	if !st.Applied && errText == "" {
		errText = "not applied"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET config_status_version = ?, config_status_error = ? WHERE id = ?`,
		st.Version, errText, agentID)
	return err
}

type DiffLine struct {
	Kind string // "same", "add" or "del"
	Text string
}

// Diff is a line diff of a → b (Myers, via go-diff, so a 256 KiB config needs
// no quadratic table).
func Diff(a, b string) []DiffLine {
	dmp := diffmatchpatch.New()
	aChars, bChars, lines := dmp.DiffLinesToChars(strings.TrimSuffix(a, "\n")+"\n", strings.TrimSuffix(b, "\n")+"\n")
	diffs := dmp.DiffCharsToLines(dmp.DiffMain(aChars, bChars, false), lines)

	var out []DiffLine
	for _, chunk := range diffs {
		kind := "same"
		switch chunk.Type {
		case diffmatchpatch.DiffDelete:
			kind = "del"
		case diffmatchpatch.DiffInsert:
			kind = "add"
		}
		for _, text := range strings.Split(strings.TrimSuffix(chunk.Text, "\n"), "\n") {
			out = append(out, DiffLine{Kind: kind, Text: text})
		}
	}
	return out
}
