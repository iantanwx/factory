package rpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Audit alert kinds this unit raises.
const (
	AlertHostVerbFromCheckout = "host_verb_from_checkout"
	AlertTokenOutsideCheckout = "token_outside_checkout"
)

// AuditAlert is one audit row and the alert it raises. The detail and the
// body pass through the redactor before they are stored (KTD27).
type AuditAlert struct {
	Actor   string
	Project string
	Item    string
	Kind    string // the audit action and the alert kind
	Subject string // the alert subject; the dedupe key is Kind + ":" + Subject
	Body    string
	Detail  map[string]any
}

// WriteAuditAlert writes the audit row and raises or counts up the alert in
// one transaction. The inbox reads the alerts table.
func WriteAuditAlert(ctx context.Context, st *store.Store, red *secrets.Redactor, a AuditAlert) error {
	detail := a.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	body := a.Body
	if red != nil {
		raw = []byte(red.Redact(string(raw)))
		if !json.Valid(raw) {
			raw = []byte(`{"redacted":true}`)
		}
		body = red.Redact(body)
	}
	return st.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO audit (at, actor, project, item, action, detail) VALUES (?, ?, ?, ?, ?, ?)`,
			now, a.Actor, a.Project, a.Item, a.Kind, string(raw)); err != nil {
			return fmt.Errorf("rpc: write the audit row: %w", err)
		}
		_, err := tx.Exec(`INSERT INTO alerts (project, kind, subject, dedupe_key, body, first_at, last_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (dedupe_key) WHERE state = 'open'
			DO UPDATE SET count = count + 1, last_at = excluded.last_at, body = excluded.body`,
			a.Project, a.Kind, a.Subject, a.Kind+":"+a.Subject, body, now, now)
		if err != nil {
			return fmt.Errorf("rpc: raise the alert: %w", err)
		}
		return nil
	})
}
