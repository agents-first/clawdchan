// Package store is the SQLite-backed persistence layer. The core owns the
// store; hosts read through the Node API rather than touching the DB directly.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/pairing"
)

// Store is the aggregate persistence interface. A single SQLite file backs all
// of it. Tables: identity, peers, threads, envelopes, outbox.
type Store interface {
	LoadIdentity(ctx context.Context) (*identity.Identity, error)
	SaveIdentity(ctx context.Context, id *identity.Identity) error

	UpsertPeer(ctx context.Context, p pairing.Peer) error
	ListPeers(ctx context.Context) ([]pairing.Peer, error)
	GetPeer(ctx context.Context, nodeID identity.NodeID) (pairing.Peer, error)
	RevokePeer(ctx context.Context, nodeID identity.NodeID) error
	// SetPeerAlias overrides the peer's display alias locally without
	// touching the rest of the record. Returns ErrNotFound if the peer
	// doesn't exist.
	SetPeerAlias(ctx context.Context, nodeID identity.NodeID, alias string) error
	// DeletePeer hard-removes the peer row plus any threads/envelopes/
	// outbox entries tied to that peer. Use for full forget; prefer
	// RevokePeer if you just want to stop trusting but keep history.
	DeletePeer(ctx context.Context, nodeID identity.NodeID) error

	CreateThread(ctx context.Context, t Thread) error
	ListThreads(ctx context.Context) ([]Thread, error)
	GetThread(ctx context.Context, id envelope.ThreadID) (Thread, error)

	AppendEnvelope(ctx context.Context, env envelope.Envelope, delivered bool) error
	ListEnvelopes(ctx context.Context, thread envelope.ThreadID, sinceMs int64) ([]envelope.Envelope, error)

	EnqueueOutbox(ctx context.Context, peer identity.NodeID, env envelope.Envelope) error
	DrainOutbox(ctx context.Context, peer identity.NodeID) ([]envelope.Envelope, error)

	// PurgeConversations wipes threads, envelopes, and outbox. Identity and
	// peers (pairings) are preserved. Called by hosts that want
	// session-scoped thread state — e.g. clawdchan-mcp at boot, so a fresh
	// Claude Code session starts with an empty thread list.
	PurgeConversations(ctx context.Context) error

	Close() error
}

// Thread is the stored descriptor for a conversation with one peer.
type Thread struct {
	ID        envelope.ThreadID
	PeerID    identity.NodeID
	Topic     string
	CreatedMs int64
}

// ErrNotFound is returned by getters when a row does not exist.
var ErrNotFound = errors.New("store: not found")

const schema = `
CREATE TABLE IF NOT EXISTS identity (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    sign_pub    BLOB NOT NULL,
    sign_priv   BLOB NOT NULL,
    kex_pub     BLOB NOT NULL,
    kex_priv    BLOB NOT NULL,
    created_ms  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS peers (
    node_id         BLOB PRIMARY KEY,
    kex_pub         BLOB NOT NULL,
    alias           TEXT NOT NULL,
    trust           INTEGER NOT NULL,
    human_reachable INTEGER NOT NULL,
    paired_ms       INTEGER NOT NULL,
    sas0 TEXT NOT NULL, sas1 TEXT NOT NULL, sas2 TEXT NOT NULL, sas3 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS threads (
    id          BLOB PRIMARY KEY,
    peer_id     BLOB NOT NULL,
    topic       TEXT NOT NULL,
    created_ms  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS envelopes (
    envelope_id BLOB PRIMARY KEY,
    thread_id   BLOB NOT NULL,
    from_node   BLOB NOT NULL,
    from_role   INTEGER NOT NULL,
    intent      INTEGER NOT NULL,
    created_ms  INTEGER NOT NULL,
    blob        BLOB NOT NULL,
    delivered   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS envelopes_thread_ms ON envelopes(thread_id, created_ms);

CREATE TABLE IF NOT EXISTS outbox (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    peer_node   BLOB NOT NULL,
    envelope_id BLOB NOT NULL,
    blob        BLOB NOT NULL,
    created_ms  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS outbox_peer ON outbox(peer_node);
`

type sqliteStore struct {
	db *sql.DB
}

// Open opens or creates a SQLite-backed Store at path.
func Open(path string) (Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragmas: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func (s *sqliteStore) LoadIdentity(ctx context.Context) (*identity.Identity, error) {
	row := s.db.QueryRowContext(ctx, `SELECT sign_pub, sign_priv, kex_pub, kex_priv FROM identity WHERE id = 1`)
	var sp, sv, kp, kv []byte
	if err := row.Scan(&sp, &sv, &kp, &kv); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load identity: %w", err)
	}
	id := &identity.Identity{}
	copy(id.SigningPublic[:], sp)
	id.SigningPrivate = append([]byte(nil), sv...)
	copy(id.KexPublic[:], kp)
	copy(id.KexPrivate[:], kv)
	return id, nil
}

func (s *sqliteStore) SaveIdentity(ctx context.Context, id *identity.Identity) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO identity (id, sign_pub, sign_priv, kex_pub, kex_priv, created_ms)
        VALUES (1, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            sign_pub=excluded.sign_pub,
            sign_priv=excluded.sign_priv,
            kex_pub=excluded.kex_pub,
            kex_priv=excluded.kex_priv
    `, id.SigningPublic[:], []byte(id.SigningPrivate), id.KexPublic[:], id.KexPrivate[:], time.Now().UnixMilli())
	return err
}

func (s *sqliteStore) UpsertPeer(ctx context.Context, p pairing.Peer) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO peers (node_id, kex_pub, alias, trust, human_reachable, paired_ms, sas0, sas1, sas2, sas3)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(node_id) DO UPDATE SET
            kex_pub=excluded.kex_pub,
            alias=excluded.alias,
            trust=excluded.trust,
            human_reachable=excluded.human_reachable,
            paired_ms=excluded.paired_ms,
            sas0=excluded.sas0, sas1=excluded.sas1, sas2=excluded.sas2, sas3=excluded.sas3
    `, p.NodeID[:], p.KexPub[:], p.Alias, int(p.Trust), boolToInt(p.HumanReachable), p.PairedAtMs,
		p.SAS[0], p.SAS[1], p.SAS[2], p.SAS[3])
	return err
}

func (s *sqliteStore) ListPeers(ctx context.Context) ([]pairing.Peer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, kex_pub, alias, trust, human_reachable, paired_ms, sas0, sas1, sas2, sas3 FROM peers ORDER BY paired_ms ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pairing.Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GetPeer(ctx context.Context, nodeID identity.NodeID) (pairing.Peer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT node_id, kex_pub, alias, trust, human_reachable, paired_ms, sas0, sas1, sas2, sas3 FROM peers WHERE node_id = ?`, nodeID[:])
	p, err := scanPeer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return pairing.Peer{}, ErrNotFound
	}
	return p, err
}

func (s *sqliteStore) RevokePeer(ctx context.Context, nodeID identity.NodeID) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET trust = ? WHERE node_id = ?`, int(pairing.TrustRevoked), nodeID[:])
	return err
}

func (s *sqliteStore) SetPeerAlias(ctx context.Context, nodeID identity.NodeID, alias string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET alias = ? WHERE node_id = ?`, alias, nodeID[:])
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *sqliteStore) DeletePeer(ctx context.Context, nodeID identity.NodeID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Threads carry a peer FK via peer_id; envelopes are thread-scoped; the
	// outbox is peer-keyed. Purge all three before dropping the peer row.
	if _, err := tx.ExecContext(ctx, `DELETE FROM envelopes WHERE thread_id IN (SELECT id FROM threads WHERE peer_id = ?)`, nodeID[:]); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM threads WHERE peer_id = ?`, nodeID[:]); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE peer_node = ?`, nodeID[:]); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM peers WHERE node_id = ?`, nodeID[:])
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *sqliteStore) CreateThread(ctx context.Context, t Thread) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO threads (id, peer_id, topic, created_ms) VALUES (?, ?, ?, ?)`,
		t.ID[:], t.PeerID[:], t.Topic, t.CreatedMs)
	return err
}

func (s *sqliteStore) ListThreads(ctx context.Context) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, peer_id, topic, created_ms FROM threads ORDER BY created_ms ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		var t Thread
		var id, peer []byte
		if err := rows.Scan(&id, &peer, &t.Topic, &t.CreatedMs); err != nil {
			return nil, err
		}
		copy(t.ID[:], id)
		copy(t.PeerID[:], peer)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GetThread(ctx context.Context, id envelope.ThreadID) (Thread, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, peer_id, topic, created_ms FROM threads WHERE id = ?`, id[:])
	var t Thread
	var tid, peer []byte
	if err := row.Scan(&tid, &peer, &t.Topic, &t.CreatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Thread{}, ErrNotFound
		}
		return Thread{}, err
	}
	copy(t.ID[:], tid)
	copy(t.PeerID[:], peer)
	return t, nil
}

func (s *sqliteStore) AppendEnvelope(ctx context.Context, env envelope.Envelope, delivered bool) error {
	blob, err := envelope.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO envelopes (envelope_id, thread_id, from_node, from_role, intent, created_ms, blob, delivered)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(envelope_id) DO NOTHING
    `, env.EnvelopeID[:], env.ThreadID[:], env.From.NodeID[:], int(env.From.Role), int(env.Intent),
		env.CreatedAtMs, blob, boolToInt(delivered))
	return err
}

func (s *sqliteStore) ListEnvelopes(ctx context.Context, thread envelope.ThreadID, sinceMs int64) ([]envelope.Envelope, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT blob FROM envelopes WHERE thread_id = ? AND created_ms > ? ORDER BY created_ms ASC`, thread[:], sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []envelope.Envelope
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		env, err := envelope.Unmarshal(blob)
		if err != nil {
			return nil, fmt.Errorf("decode stored envelope: %w", err)
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

func (s *sqliteStore) EnqueueOutbox(ctx context.Context, peer identity.NodeID, env envelope.Envelope) error {
	blob, err := envelope.Marshal(env)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO outbox (peer_node, envelope_id, blob, created_ms) VALUES (?, ?, ?, ?)`,
		peer[:], env.EnvelopeID[:], blob, time.Now().UnixMilli())
	return err
}

func (s *sqliteStore) DrainOutbox(ctx context.Context, peer identity.NodeID) ([]envelope.Envelope, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, blob FROM outbox WHERE peer_node = ? ORDER BY id ASC`, peer[:])
	if err != nil {
		return nil, err
	}
	var ids []int64
	var envs []envelope.Envelope
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			rows.Close()
			return nil, err
		}
		env, err := envelope.Unmarshal(blob)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		envs = append(envs, env)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return envs, nil
}

func (s *sqliteStore) PurgeConversations(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM envelopes`,
		`DELETE FROM threads`,
		`DELETE FROM outbox`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("purge: %w", err)
		}
	}
	return tx.Commit()
}

// scanPeer works with *sql.Row and *sql.Rows since both have a Scan(dest...) method.
type scanner interface {
	Scan(dest ...any) error
}

func scanPeer(r scanner) (pairing.Peer, error) {
	var p pairing.Peer
	var nodeID, kexPub []byte
	var trust, reachable int
	if err := r.Scan(&nodeID, &kexPub, &p.Alias, &trust, &reachable, &p.PairedAtMs,
		&p.SAS[0], &p.SAS[1], &p.SAS[2], &p.SAS[3]); err != nil {
		return pairing.Peer{}, err
	}
	copy(p.NodeID[:], nodeID)
	copy(p.KexPub[:], kexPub)
	p.Trust = pairing.Trust(trust)
	p.HumanReachable = reachable != 0
	return p, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
