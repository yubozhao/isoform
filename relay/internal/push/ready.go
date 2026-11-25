package push

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// KindReady is a background push that wakes a phone to fetch a brief the
// computer just stored for it (PROTOCOL.md 9.5). It carries no brief id,
// title, slot or time: the phone reads the newest brief it is due and
// compares it with the one it holds.
const KindReady = "ready"

// readyExpiry is how long a ready wake is worth sending after it is due. Past
// it, the phone's own reads (on open, its scheduled wakes) are the guarantee.
const readyExpiry = 6 * time.Hour

// EnqueueReady queues one ready wake for a device with a registration, first
// due at due (not before now). A device keeps at most one queued ready wake:
// a newer one replaces it. A device without a registration gets no row.
func EnqueueReady(ctx context.Context, q Querier, deviceID string, now, due time.Time) error {
	var registered int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM push_devices WHERE device_id=?", deviceID).Scan(&registered); err != nil {
		return err
	}
	if registered == 0 {
		return nil
	}
	if due.Before(now) {
		due = now
	}
	if err := cancelWork(ctx, q, "device_id=? AND kind=? AND state='queued'", deviceID, KindReady); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO push_outbox(device_id,event_id,notification_id,kind,state,created_at,expires_at,next_at)
	VALUES(?,?,?,?,'queued',?,?,?)`,
		deviceID, uuid.NewString(), uuid.NewString(), KindReady, now.UnixMilli(), due.Add(readyExpiry).UnixMilli(), due.UnixMilli())
	return err
}
