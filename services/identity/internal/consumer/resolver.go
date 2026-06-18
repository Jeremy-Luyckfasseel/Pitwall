package consumer

import (
	"context"
	"database/sql"

	gopdb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/persistence"
	librelay "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/relay"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/persistence"
)

// TxResolver is the production Resolver: it runs the whole consume-side operation in
// ONE transaction so a crash can neither double-mint nor ack-without-replying
// (consumer-side ECST, M6/NFR6):
//
//	inbox-dedupe (envelope id) -> resolve-or-mint (UNIQUE email) ->
//	enqueue the identity.resolved reply to the outbox -> mark inbox -> commit
//
// The relay (a separate loop) publishes the enqueued reply to identity.events and
// marks it sent only after a broker confirm-ack, so the reply survives a bus outage.
type TxResolver struct {
	DB          *sql.DB
	Store       *persistence.Store
	Outbox      *gopdb.OutboxStore
	ValidateOut func([]byte) error // validate-on-publish for the reply (belt-and-suspenders before the relay)
	MintID      func() string      // candidate masterId generator (uuid.NewString -> lowercase v4)
	Now         func() string      // wire-format timestamp stamp (defaults to now)
	Source      string             // reply envelope source ("identity")
	ResolvedKey string             // identity.resolved routing key (topology, injected)
}

// Resolve implements the Resolver interface. A redelivered envelope id is a no-op
// (Duplicate=true) — no second mint, no second reply (AC3).
func (r *TxResolver) Resolve(ctx context.Context, env messaging.IncomingEnvelope, data domain.LookupData) (ResolveResult, error) {
	now := r.now()
	var res ResolveResult

	err := persistence.WithinTx(ctx, r.DB, func(tx *sql.Tx) error {
		seen, err := gopdb.InboxHasSeen(ctx, tx, env.ID)
		if err != nil {
			return err
		}
		if seen {
			res = ResolveResult{Duplicate: true}
			return nil
		}

		masterID, minted, err := r.Store.ResolveOrMint(ctx, tx, data.Email, r.MintID(), now)
		if err != nil {
			return err
		}

		reply := domain.BuildResolved(r.ResolvedKey, r.Source, now, domain.LookupRequest{
			RequestID:     data.RequestID,
			Email:         data.Email,
			EnvelopeID:    env.ID,
			CorrelationID: env.CorrelationID,
		}, masterID)
		if err := librelay.EnqueueEnvelope(tx, r.Outbox, r.ValidateOut, reply); err != nil {
			return err
		}

		if err := gopdb.InboxMarkSeen(ctx, tx, env.ID, env.Type, now); err != nil {
			return err
		}
		res = ResolveResult{MasterID: masterID, Minted: minted}
		return nil
	})
	if err != nil {
		return ResolveResult{}, err
	}
	return res, nil
}

func (r *TxResolver) now() string {
	if r.Now != nil {
		return r.Now()
	}
	return wireNow()
}
