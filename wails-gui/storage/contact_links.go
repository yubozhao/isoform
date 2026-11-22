package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const legacyContactPayload = `{"_legacy":true}`

func (s *Store) migrateContactLinks() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
        CREATE TABLE IF NOT EXISTS imported_contact_cards (
            external_ref TEXT PRIMARY KEY,
            payload TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS contact_handle_provenance (
            external_ref TEXT NOT NULL REFERENCES imported_contact_cards(external_ref) ON DELETE CASCADE,
            handle_id INTEGER NOT NULL REFERENCES app_handles(id) ON DELETE CASCADE,
            PRIMARY KEY (external_ref, handle_id)
        );
        CREATE TABLE IF NOT EXISTS principal_contact_links (
            principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
            external_ref TEXT NOT NULL UNIQUE REFERENCES imported_contact_cards(external_ref) ON DELETE CASCADE,
            receipt TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS principal_contact_link_updates (
            principal_id TEXT NOT NULL REFERENCES principal_contact_links(principal_id) ON DELETE CASCADE,
            app TEXT NOT NULL,
            session_id TEXT NOT NULL,
            message_count INTEGER NOT NULL,
            first_seen_at INTEGER NOT NULL,
            last_seen_at INTEGER NOT NULL,
            PRIMARY KEY(principal_id,app,session_id)
        );
    `)
	if err != nil {
		return err
	}
	// The v61 importer wrote the same nonempty identifier into both fields,
	// even when it deduped a card into a curated person. Seed only that shape
	// so manual editor rows stay unowned. Phone/email ownership additionally
	// needs the current source snapshot; v61 has no field-level history.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO imported_contact_cards(external_ref,payload)
		SELECT handle_id,? FROM app_handles WHERE app='contacts' AND handle_id<>'' AND display_handle=handle_id`, legacyContactPayload); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO contact_handle_provenance(external_ref,handle_id)
		SELECT handle_id,id FROM app_handles WHERE app='contacts' AND handle_id<>'' AND display_handle=handle_id`); err != nil {
		return err
	}
	return tx.Commit()
}

// AdoptLegacyContactHandles attributes only surviving source fields on a
// native-import-created person. Curated owners and mixed vCard/native profiles
// are ambiguous in v61 and keep their pre-existing phone/email bindings. Only
// equal, nonempty ID/display pairs match the v61 importer; other shapes are
// manual bindings even when a native field matches one of their values. Run
// this for the whole snapshot before checkpointing any card, so shared native
// fields receive every source contribution regardless of enumeration order.
func (s *Store) AdoptLegacyContactHandles(ctx context.Context, externalRef string, phones, emails []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, group := range []struct {
		app    string
		values []string
	}{{"imessage", phones}, {"email", emails}} {
		for _, value := range group.values {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO contact_handle_provenance(external_ref,handle_id)
				SELECT c.external_ref,h.id FROM imported_contact_cards c
				JOIN app_handles native ON native.app='contacts' AND native.handle_id=c.external_ref
					AND native.display_handle=native.handle_id
				JOIN principals p ON p.id=native.principal_id
				JOIN app_handles h ON h.principal_id=p.id
				WHERE c.external_ref=? AND c.payload=?
				AND p.source='system_contacts' AND p.created_from='system_contacts'
				AND EXISTS(SELECT 1 FROM app_handles anchor WHERE anchor.principal_id=p.id
					AND anchor.app='contacts' AND anchor.handle_id=p.external_ref AND anchor.display_handle=anchor.handle_id)
				AND NOT EXISTS(SELECT 1 FROM app_handles other WHERE other.principal_id=p.id AND other.app='vcard')
				AND h.app=? AND h.handle_id<>'' AND h.handle_id=? AND h.display_handle=?`, externalRef, legacyContactPayload, group.app, value, value); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type ImportedContactCard struct {
	ExternalRef string
	Payload     string
	Legacy      bool
}

func (s *Store) ListImportedContactCards(ctx context.Context) ([]ImportedContactCard, error) {
	rows, err := s.committedReader().QueryContext(ctx, `SELECT external_ref,payload FROM imported_contact_cards ORDER BY external_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImportedContactCard{}
	for rows.Next() {
		var c ImportedContactCard
		if err := rows.Scan(&c.ExternalRef, &c.Payload); err != nil {
			return nil, err
		}
		c.Legacy = c.Payload == legacyContactPayload
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveImportedContactCard checkpoints only a successfully applied card. The
// provenance list contains handles this source introduced, never pre-existing
// curated bindings. Removed fields lose only their source-owned handles.
func (s *Store) SaveImportedContactCard(ctx context.Context, c ImportedContactCard, added, retained []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO imported_contact_cards(external_ref,payload) VALUES(?,?) ON CONFLICT(external_ref) DO UPDATE SET payload=excluded.payload`, c.ExternalRef, c.Payload); err != nil {
		return err
	}
	for _, id := range added {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO contact_handle_provenance(external_ref,handle_id) VALUES(?,?)`, c.ExternalRef, id); err != nil {
			return err
		}
	}
	// A shared imported number can belong to several source cards on the
	// same resolved person. Track each contribution, while leaving handles
	// that predate every import unmarked.
	for _, id := range retained {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO contact_handle_provenance(external_ref,handle_id) SELECT ?,handle_id FROM contact_handle_provenance WHERE handle_id=? LIMIT 1`, c.ExternalRef, id); err != nil {
			return err
		}
	}
	keep := map[int64]bool{}
	for _, id := range retained {
		keep[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT handle_id FROM contact_handle_provenance WHERE external_ref=?`, c.ExternalRef)
	if err != nil {
		return err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM contact_handle_provenance WHERE external_ref=? AND handle_id=?`, c.ExternalRef, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_handles WHERE id=? AND NOT EXISTS(SELECT 1 FROM contact_handle_provenance WHERE handle_id=?)`, id, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RemoveImportedContactCards removes source-owned handles, including those on
// curated people. Callers unlink explicit links first and delete only imported
// principals, preserving captured people and their history.
func (s *Store) RemoveImportedContactCards(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_handles WHERE id IN (SELECT handle_id FROM contact_handle_provenance); DELETE FROM imported_contact_cards`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LinkedContactRef(ctx context.Context, principalID string) (string, error) {
	var ref string
	err := s.db.QueryRowContext(ctx, `SELECT external_ref FROM principal_contact_links WHERE principal_id=?`, principalID).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ref, err
}

// LinkedContactRefs returns the same principal-to-contact links as
// LinkedContactRef, grouped for a single People read.
func (s *Store) LinkedContactRefs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT principal_id, external_ref
		  FROM principal_contact_links
		 ORDER BY principal_id`)
	if err != nil {
		return nil, fmt.Errorf("list principal contact links: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var principalID, externalRef string
		if err := rows.Scan(&principalID, &externalRef); err != nil {
			return nil, fmt.Errorf("scan principal contact link: %w", err)
		}
		out[principalID] = externalRef
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter principal contact links: %w", err)
	}
	return out, nil
}

type contactLinkReceipt struct {
	Source              PrincipalRow
	Destination         PrincipalRow
	Handles             []AppHandleRow
	AddedAliases        []string
	ContactRefs         []string
	SourceActivity      contactActivitySnapshot
	DestinationActivity contactActivitySnapshot
	SourceResearch      *PrincipalResearchRow
	SourceVoiceIDs      []int64
}

// MergeContactIntoPrincipal uses the same transaction as MergePrincipals and
// records the source contribution and destination baseline. Activity captured
// after the link is tracked separately so unlink preserves later observations.
func (s *Store) MergeContactIntoPrincipal(ctx context.Context, srcID, dstID, externalRef string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	src, err := getPrincipalTx(ctx, tx, srcID)
	if err != nil {
		return err
	}
	dst, err := getPrincipalTx(ctx, tx, dstID)
	if err != nil {
		return err
	}
	if srcID == dstID || src.Kind == PrincipalKindSelf {
		return errors.New("choose a separate imported contact")
	}
	var linked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM principal_contact_links WHERE principal_id IN (?,?) OR external_ref=?)`, srcID, dstID, externalRef).Scan(&linked); err != nil {
		return err
	}
	if linked {
		return errors.New("unlink the current contact first")
	}
	receipt := contactLinkReceipt{Source: src, Destination: dst}
	receipt.SourceResearch, err = snapshotContactResearchTx(ctx, tx, srcID)
	if err != nil {
		return err
	}
	voiceRows, err := tx.QueryContext(ctx, `SELECT id FROM principal_voice_embeddings WHERE principal_id=?`, srcID)
	if err != nil {
		return err
	}
	for voiceRows.Next() {
		var id int64
		if err := voiceRows.Scan(&id); err != nil {
			voiceRows.Close()
			return err
		}
		receipt.SourceVoiceIDs = append(receipt.SourceVoiceIDs, id)
	}
	if err := voiceRows.Err(); err != nil {
		voiceRows.Close()
		return err
	}
	voiceRows.Close()
	if _, err := tx.ExecContext(ctx, `UPDATE principal_voice_embeddings SET principal_id=? WHERE principal_id=?`, dstID, srcID); err != nil {
		return err
	}

	receipt.SourceActivity, err = snapshotContactActivity(ctx, tx, srcID)
	if err != nil {
		return err
	}
	receipt.DestinationActivity, err = snapshotContactActivity(ctx, tx, dstID)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,principal_id,app,handle_id,display_handle,created_at FROM app_handles WHERE principal_id=?`, srcID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h AppHandleRow
		if err := rows.Scan(&h.ID, &h.PrincipalID, &h.App, &h.HandleID, &h.DisplayHandle, &h.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		receipt.Handles = append(receipt.Handles, h)
		if h.App == "contacts" {
			receipt.ContactRefs = append(receipt.ContactRefs, h.HandleID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	existing := map[string]bool{}
	for _, a := range dst.Aliases {
		existing[a] = true
	}
	for _, a := range append(src.Aliases, src.DisplayName) {
		if a != "" && !existing[a] {
			receipt.AddedAliases = append(receipt.AddedAliases, a)
			existing[a] = true
		}
	}
	if _, err := mergePrincipalsTx(ctx, tx, srcID, dstID); err != nil {
		return err
	}
	if err := writeAliasesTx(ctx, tx, dstID, receipt.AddedAliases); err != nil {
		return err
	}
	if dst.Kind == PrincipalKindAnonymous || dst.Kind == PrincipalKindContentSubject {
		if _, err := tx.ExecContext(ctx, `UPDATE principals SET kind='contact' WHERE id=?`, dstID); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO principal_contact_links(principal_id,external_ref,receipt) VALUES(?,?,?)`, dstID, externalRef, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnlinkContactPrincipal(ctx context.Context, principalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT receipt FROM principal_contact_links WHERE principal_id=?`, principalID).Scan(&raw); err != nil {
		return err
	}
	var r contactLinkReceipt
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return fmt.Errorf("read contact link receipt: %w", err)
	}
	p := r.Source
	if _, err := tx.ExecContext(ctx, `INSERT INTO principals(id,kind,display_name,relationship,notes,created_from,source,external_ref,confidence,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Kind, p.DisplayName, p.Relationship, p.Notes, p.CreatedFrom, p.Source, p.ExternalRef, p.Confidence, p.CreatedAt, time.Now().UnixMilli()); err != nil {
		return err
	}
	if err := writeAliasesTx(ctx, tx, p.ID, p.Aliases); err != nil {
		return err
	}
	// Include later syncs for every source card on the merged principal.
	for _, ref := range r.ContactRefs {
		if _, err := tx.ExecContext(ctx, `UPDATE app_handles SET principal_id=? WHERE principal_id=? AND id IN (SELECT handle_id FROM contact_handle_provenance WHERE external_ref=?)`, p.ID, principalID, ref); err != nil {
			return err
		}
	}

	for _, h := range r.Handles {
		if _, err := tx.ExecContext(ctx, `UPDATE app_handles SET principal_id=? WHERE id=? AND principal_id=?`, p.ID, h.ID, principalID); err != nil {
			return err
		}
	}
	for _, a := range r.AddedAliases {
		if _, err := tx.ExecContext(ctx, `DELETE FROM principal_aliases WHERE principal_id=? AND alias=?`, principalID, a); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE principals SET kind=?,updated_at=? WHERE id=?`, r.Destination.Kind, time.Now().UnixMilli(), principalID); err != nil {
		return err
	}
	for _, id := range r.SourceVoiceIDs {
		// A sample the user deleted while linked stays deleted.
		if _, err := tx.ExecContext(ctx, `UPDATE principal_voice_embeddings SET principal_id=? WHERE id=? AND principal_id=?`, r.Source.ID, id, principalID); err != nil {
			return err
		}
	}
	if err := restoreContactResearchTx(ctx, tx, r.SourceResearch); err != nil {
		return err
	}
	if err := restoreContactActivity(ctx, tx, principalID, r); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM principal_contact_links WHERE principal_id=?`, principalID); err != nil {
		return err
	}
	return tx.Commit()
}

// BindImportedContactHandle stores a new binding and its source ownership in
// one transaction, so cancellation cannot leave an untracked imported handle.
func (s *Store) BindImportedContactHandle(ctx context.Context, principalID, externalRef, app, handle string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT principal_id FROM app_handles WHERE app=? AND (handle_id=? OR display_handle=?) LIMIT 1`, app, handle, handle).Scan(&owner)
	if err == nil {
		if owner != principalID {
			return errors.New("handle already belongs to another person")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO imported_contact_cards(external_ref,payload) VALUES(?,'{}')`, externalRef); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO app_handles(principal_id,app,handle_id,display_handle,created_at) VALUES(?,?,?,?,?)`, principalID, app, handle, handle, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO contact_handle_provenance(external_ref,handle_id) VALUES(?,?)`, externalRef, id); err != nil {
		return err
	}
	return tx.Commit()
}

// A deleted native card withdraws its source-owned identity. Preserve people
// who have captured history, research or user-authored profile details.
func (s *Store) RemoveImportedContactCard(ctx context.Context, externalRef string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_handles
		WHERE id IN (SELECT handle_id FROM contact_handle_provenance WHERE external_ref=?)
		AND NOT EXISTS(SELECT 1 FROM contact_handle_provenance other WHERE other.handle_id=app_handles.id AND other.external_ref!=?)`, externalRef, externalRef); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM imported_contact_cards WHERE external_ref=?`, externalRef); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM principals WHERE source='system_contacts' AND external_ref=?
		AND notes='' AND relationship=''
		AND NOT EXISTS(SELECT 1 FROM app_handles WHERE principal_id=principals.id)
		AND NOT EXISTS(SELECT 1 FROM principal_aliases WHERE principal_id=principals.id)
		AND NOT EXISTS(SELECT 1 FROM principal_sightings WHERE principal_id=principals.id)
		AND NOT EXISTS(SELECT 1 FROM principal_session_sightings WHERE principal_id=principals.id)
		AND NOT EXISTS(SELECT 1 FROM principal_voice_embeddings WHERE principal_id=principals.id)
		AND NOT EXISTS(SELECT 1 FROM principal_research WHERE principal_id=principals.id)`, externalRef); err != nil {
		return err
	}
	return tx.Commit()
}
