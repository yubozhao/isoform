package push

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/wire"
)

// KindWithdraw is a background push that takes delivered notifications away
// from a phone: the person read them somewhere else (PROTOCOL.md 9.4). It
// carries only notice ids, which the relay already saw on their way out.
const KindWithdraw = "withdraw"

// MaxWithdrawIDs bounds one withdraw push.
const MaxWithdrawIDs = 20

// withdrawExpiry is how long a withdraw push is worth sending: past it, the
// phone's own read of GET /notices/read has cleared the notification.
const withdrawExpiry = time.Hour

var briefNoticePattern = regexp.MustCompile(`^brief:[A-Za-z0-9_-]{1,64}$`)

// ValidWithdrawIDs checks a withdraw push's ids: notice ids (UUIDs) or
// brief:<briefId>, at most MaxWithdrawIDs, none repeated.
func ValidWithdrawIDs(ids []string) bool {
	if len(ids) == 0 || len(ids) > MaxWithdrawIDs {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || !(wire.ValidUUID(id) || briefNoticePattern.MatchString(id)) {
			return false
		}
		seen[id] = true
	}
	return true
}

// WithdrawIDs reads the ids a withdraw claim carries (its CT field).
func WithdrawIDs(ct string) []string {
	if ct == "" {
		return nil
	}
	return strings.Split(ct, " ")
}

// EnqueueWithdraw queues one withdraw push for a device with a registration;
// a device without one gets no row and no error.
func EnqueueWithdraw(ctx context.Context, q Querier, deviceID string, ids []string, now time.Time) error {
	if !ValidWithdrawIDs(ids) {
		return ErrInvalidNotice
	}
	var registered int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM push_devices WHERE device_id=?", deviceID).Scan(&registered); err != nil {
		return err
	}
	if registered == 0 {
		return nil
	}
	_, err := q.ExecContext(ctx, `INSERT INTO push_outbox(device_id,event_id,notification_id,kind,state,created_at,expires_at,next_at,ct)
	VALUES(?,?,?,?,'queued',?,?,?,?)`,
		deviceID, uuid.NewString(), uuid.NewString(), KindWithdraw, now.UnixMilli(), now.Add(withdrawExpiry).UnixMilli(), now.UnixMilli(),
		strings.Join(ids, " "))
	return err
}
