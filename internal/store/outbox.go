package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// lastDelivered picks each item's latest delivered message.
const lastDelivered = "WHERE id IN (SELECT max(id) FROM deliveries WHERE state = 'delivered' GROUP BY task_id)"

const deliveryColumns = "id, task_id, terminal, kind, body, state, attempts, last_error, created_at, delivered_at"

func scanDelivery(r scanner) (Delivery, error) {
	var d Delivery
	err := r.Scan(&d.ID, &d.TaskID, &d.Terminal, &d.Kind, &d.Body, &d.State, &d.Attempts, &d.LastError,
		&d.CreatedAt, &d.DeliveredAt)
	return d, err
}

// deliveries returns the matching deliveries by item, oldest first.
func (s *Store) deliveries(where string, args ...any) (map[int64][]Delivery, error) {
	rows, err := s.db.Query("SELECT "+deliveryColumns+" FROM deliveries "+where+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out[d.TaskID] = append(out[d.TaskID], d)
	}
	return out, rows.Err()
}

// Enqueue puts a message for an agent's terminal in the outbox, pending until it's typed in.
func (s *Store) Enqueue(taskID int64, terminal, kind, body string) (*Delivery, error) {
	if _, err := s.Get(taskID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(terminal) == "" {
		return nil, errors.New("a delivery needs a terminal")
	}
	if !slices.Contains(DeliveryKinds, kind) {
		return nil, fmt.Errorf("delivery kind must be one of %s", strings.Join(DeliveryKinds, ", "))
	}
	res, err := s.db.Exec("INSERT INTO deliveries (task_id, terminal, kind, body, created_at) VALUES (?,?,?,?,?)",
		taskID, terminal, kind, body, s.now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	d, err := scanDelivery(s.db.QueryRow("SELECT "+deliveryColumns+" FROM deliveries WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Claim takes the pending deliveries (for one terminal, or every one when terminal is "")
// so no other `condoo deliver` sends them meanwhile, oldest first. A claim older than
// stale is taken to be from a deliverer that died, and is taken over.
func (s *Store) Claim(terminal string, stale time.Duration) ([]Delivery, error) {
	now := s.now()
	q := "UPDATE deliveries SET claimed_at = ? WHERE state = 'pending' AND (claimed_at IS NULL OR claimed_at < ?)"
	args := []any{now, now - stale.Seconds()}
	if terminal != "" {
		q, args = q+" AND terminal = ?", append(args, terminal)
	}
	rows, err := s.db.Query(q+" RETURNING "+deliveryColumns, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Delivery) int { return int(a.ID - b.ID) })
	return out, nil
}

// Settle records how sending claimed deliveries went, and releases the claim:
// "delivered" (typed in), "pending" (the agent was busy: try again later) or "dead" (its
// terminal is gone).
func (s *Store) Settle(ids []int64, state, lastError string) error {
	if len(ids) == 0 {
		return nil
	}
	if !slices.Contains([]string{"delivered", "pending", "dead"}, state) {
		return fmt.Errorf("can't settle a delivery as %q", state)
	}
	var delivered any
	if state == "delivered" {
		delivered = s.now()
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{state, lastError, delivered}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.Exec("UPDATE deliveries SET state = ?, last_error = ?, delivered_at = ?, claimed_at = NULL,"+
		" attempts = attempts + 1 WHERE id IN ("+marks+")", args...)
	return err
}

// DiscardDead drops an item's deliveries whose terminal is gone, once someone else has
// been given the item: what they said is still on the item.
func (s *Store) DiscardDead(taskID int64) error {
	_, err := s.db.Exec("UPDATE deliveries SET state = 'discarded' WHERE task_id = ? AND state = 'dead'", taskID)
	return err
}
