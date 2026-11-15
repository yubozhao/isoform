package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"yansu-agent/agent/authority"
)

type AuthorityDecisionRecord struct {
	ID         string               `json:"id"`
	Evaluation authority.Evaluation `json:"evaluation"`
	CreatedAt  int64                `json:"createdAt"`
}

func (s *ActivityStore) SaveAuthorityDecision(evaluation authority.Evaluation) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("activity store is unavailable")
	}
	payload, err := json.Marshal(evaluation)
	if err != nil {
		return fmt.Errorf("encode authority evaluation: %w", err)
	}
	now := time.Now().UnixMilli()
	_, err = s.db.Exec(`
		INSERT INTO authority_decisions (
			id, policy_version, run_id, session_id, initiative_id, tool_use_id,
			tool_name, operation, capability, effect, resource, recipient,
			reversible, unattended, decision, reason, grant_id, evaluation_json,
			created_at, shadow_run_id, execution_mode
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(),
		evaluation.PolicyVersion,
		evaluation.Context.RunID,
		evaluation.Context.SessionID,
		evaluation.Context.Initiative,
		evaluation.ToolUseID,
		evaluation.Action.ToolName,
		evaluation.Action.Operation,
		evaluation.Action.Capability,
		string(evaluation.Action.Effect),
		evaluation.Action.Resource,
		evaluation.Action.Recipient,
		authorityBoolInt(evaluation.Action.Reversible),
		authorityBoolInt(evaluation.Context.Unattended),
		string(evaluation.Decision),
		evaluation.Reason,
		evaluation.GrantID,
		string(payload),
		now,
		evaluation.Context.ShadowRun,
		string(evaluation.Context.Mode),
	)
	if err != nil {
		return fmt.Errorf("save authority decision: %w", err)
	}
	return nil
}

func (s *ActivityStore) ListAuthorityDecisions(initiativeID string, limit int) ([]AuthorityDecisionRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("activity store is unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, evaluation_json, created_at FROM authority_decisions`
	args := []any{}
	if initiativeID = strings.TrimSpace(initiativeID); initiativeID != "" {
		query += ` WHERE initiative_id = ?`
		args = append(args, initiativeID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list authority decisions: %w", err)
	}
	defer rows.Close()

	out := make([]AuthorityDecisionRecord, 0)
	for rows.Next() {
		var item AuthorityDecisionRecord
		var raw string
		if err := rows.Scan(&item.ID, &raw, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan authority decision: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &item.Evaluation); err != nil {
			return nil, fmt.Errorf("decode authority decision %s: %w", item.ID, err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *App) recordAuthorityEvaluation(evaluation authority.Evaluation) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		log.Printf(
			"[authority] decision=%s capability=%s run=%s not persisted: activity store unavailable",
			evaluation.Decision,
			evaluation.Action.Capability,
			evaluation.Context.RunID,
		)
		return
	}
	if err := a.activity.store.SaveAuthorityDecision(evaluation); err != nil {
		log.Printf(
			"[authority] persist failed decision=%s capability=%s run=%s: %v",
			evaluation.Decision,
			evaluation.Action.Capability,
			evaluation.Context.RunID,
			err,
		)
	}
}

// authorizeInboundConnectorReply is the authority behind a reply to an
// inbound connector message. The grant covers only the thread the verified
// message arrived on (inboundThread), and the reply's destination must be that
// same thread, exactly. The grant alone is a prefix match, which would let a
// longer thread name through, so the exact check comes first. It grants no
// unsolicited send, no other recipient, and no other connector operation.
func (a *App) authorizeInboundConnectorReply(
	ctx context.Context,
	platform string,
	inboundThread string,
	destination string,
) error {
	platform = strings.ToLower(strings.TrimSpace(platform))
	inboundThread = strings.TrimSpace(inboundThread)
	destination = strings.TrimSpace(destination)
	capability := "connector." + platform + ".message.reply"
	action := authority.Action{
		Capability: capability,
		ToolName:   platform + "_connector",
		Operation:  "reply",
		Effect:     authority.EffectExternalWrite,
		Resource:   destination,
		Recipient:  destination,
	}
	if inboundThread == "" || destination != inboundThread {
		return a.denyConnectorAction(capability, action,
			"the reply destination is not the thread the message arrived on")
	}
	grantID := "inbound-reply:" + platform + ":" + inboundThread
	policy := authority.NewPolicy([]authority.Grant{{
		ID:             grantID,
		Capability:     capability,
		ResourcePrefix: inboundThread,
	}})
	evaluation := policy.Evaluate(ctx, authority.Context{
		RunID:      "connector-reply-" + uuid.NewString(),
		Unattended: true,
	}, action)
	a.recordAuthorityEvaluation(evaluation)
	if evaluation.Decision != authority.DecisionAllow {
		return fmt.Errorf("connector reply denied: %s", evaluation.Reason)
	}
	return nil
}

// authorizeScheduledDelivery is the authority check on an unattended run's
// result, made just before it is sent. The result may go only to the
// destination the job was configured with: a direct message to the paired
// owner on the job's notify platform. A <delivery_target> that names another
// platform, a channel or a server is refused, and the caller sends the result
// to the job's own destination instead (or nowhere, when the job has none).
func (a *App) authorizeScheduledDelivery(notifyPlatform string, sendDM bool, target *DeliveryTarget) error {
	if target == nil {
		return nil
	}
	configured := ""
	if sendDM {
		configured = strings.ToLower(strings.TrimSpace(notifyPlatform))
		if configured == "none" {
			configured = ""
		}
	}
	destination := "owner-dm"
	if strings.TrimSpace(target.Channel) != "" || strings.TrimSpace(target.Server) != "" {
		destination = "channel:" + strings.TrimSpace(target.Server) + "/" + strings.TrimSpace(target.Channel)
	}
	resource := "scheduled:" + strings.ToLower(strings.TrimSpace(target.Platform)) + ":" + destination
	capability := "connector." + strings.ToLower(strings.TrimSpace(target.Platform)) + ".message.deliver"
	action := authority.Action{
		Capability: capability,
		ToolName:   "cron_delivery",
		Operation:  "deliver",
		Effect:     authority.EffectExternalWrite,
		Resource:   resource,
		Recipient:  resource,
	}
	if configured == "" {
		return a.denyConnectorAction(capability, action, "the job has no destination configured")
	}
	grantResource := "scheduled:" + configured + ":owner-dm"
	if resource != grantResource {
		return a.denyConnectorAction(capability, action,
			"the delivery target is not the destination configured on the job")
	}
	policy := authority.NewPolicy([]authority.Grant{{
		ID:             "scheduled-delivery:" + configured,
		Capability:     capability,
		ResourcePrefix: grantResource,
	}})
	evaluation := policy.Evaluate(context.Background(), authority.Context{
		RunID:      "scheduled-delivery-" + uuid.NewString(),
		Unattended: true,
	}, action)
	a.recordAuthorityEvaluation(evaluation)
	if evaluation.Decision != authority.DecisionAllow {
		return fmt.Errorf("scheduled delivery denied: %s", evaluation.Reason)
	}
	return nil
}

// denyConnectorAction records a refusal that was decided before any grant was
// consulted, and returns it as an error.
func (a *App) denyConnectorAction(capability string, action authority.Action, reason string) error {
	evaluation := authority.Evaluation{
		Decision: authority.DecisionDeny,
		Reason:   reason,
		Context: authority.Context{
			RunID:      "connector-deny-" + uuid.NewString(),
			Unattended: true,
		},
		Action: action,
	}
	a.recordAuthorityEvaluation(evaluation)
	return fmt.Errorf("%s denied: %s", capability, reason)
}
func authorityBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
