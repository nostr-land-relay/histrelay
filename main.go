package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/fiatjaf/khatru"
	"github.com/mailru/easyjson"
	"github.com/nbd-wtf/go-nostr"
	"golang.org/x/exp/slices"
)

var acceptableEventKinds = []int{0, 3}
var keepAtMost = 10

func main() {
	var err error

	db, err := sql.Open("sqlite3", "file:./data.db?_busy_timeout=5000&_mutex=full")
	assert(err)
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS events (
		id BLOB NOT NULL PRIMARY KEY,
		pubkey BLOB NOT NULL,
		kind TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		event TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS events__created_at ON events(created_at);
	CREATE INDEX IF NOT EXISTS events__kind__created_at ON events(kind, created_at);
	CREATE INDEX IF NOT EXISTS events__pubkey__created_at ON events(pubkey, created_at);
	CREATE INDEX IF NOT EXISTS events__pubkey__kind__created_at ON events(pubkey, kind, created_at);
	`)
	assert(err)

	relay := khatru.NewRelay()
	relay.Info.Name = "history relay"
	relay.Info.Description = "keeps history of your profile and contact list"
	relay.StoreEvent = append(relay.StoreEvent,
		func(ctx context.Context, event *nostr.Event) error {
			bId, err := hex.DecodeString(event.ID)
			if err != nil {
				return err
			}
			q := db.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", bId)
			if q.Err() != nil {
				return q.Err()
			}
			var bogus int
			err = q.Scan(&bogus)
			if sql.ErrNoRows == err {
				bPk, err := hex.DecodeString(event.PubKey)
				if err != nil {
					return err
				}
				q = db.QueryRowContext(ctx, "SELECT id, created_at FROM events WHERE pubkey = ? AND kind = ? ORDER BY created_at DESC, id ASC LIMIT 1 OFFSET ?", bPk, event.Kind, keepAtMost-1)
				if q.Err() != nil {
					return q.Err()
				}
				var (
					lId        []byte
					created_at int64
				)
				err = q.Scan(&lId, &created_at)
				if err != nil {
					if err != sql.ErrNoRows {
						return err
					}
				} else {
					if created_at > int64(event.CreatedAt) || (created_at == int64(event.CreatedAt) &&
						bytes.Compare(bId, lId) == 1) {
						return fmt.Errorf("blocked: this event is too old")
					}
				}
				_, err = db.ExecContext(ctx, "INSERT INTO events VALUES (?, ?, ?, ?, ?)", bId, bPk, event.Kind, event.CreatedAt, event.String())
				if err != nil {
					return err
				}
				_, err = db.ExecContext(ctx, "DELETE FROM events WHERE id IN (SELECT id FROM events WHERE pubkey = ? AND kind = ? ORDER BY created_at DESC, id ASC LIMIT -1 OFFSET ?)", bPk, event.Kind, keepAtMost-1)
				if err != nil {
					return err
				}
				return nil
			} else if err != nil {
				return err
			} else {
				return fmt.Errorf("duplicate")
			}
		},
	)
	relay.QueryEvents = append(relay.QueryEvents,
		func(ctx context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
			ch := make(chan *nostr.Event)
			var query = make([]string, 0, 3)
			var queryParams = make([]any, 0, 5)
			if filter.Since != nil {
				queryParams = append(queryParams, int64(*filter.Since))
				query = append(query, "created_at > ?")
			}
			if filter.Until != nil {
				queryParams = append(queryParams, int64(*filter.Until))
				query = append(query, "created_at < ?")
			}
			if len(filter.Kinds) > 0 {
				var ctr = 0
				for _, k := range filter.Kinds {
					if !slices.Contains(acceptableEventKinds, k) {
						continue
					}
					queryParams = append(queryParams, k)
					ctr++
				}
				if ctr != 0 {
					query = append(query, "kind IN ("+strings.Repeat("?,", ctr-1)+"?)")
				}
			}
			if len(filter.Authors) > 0 {
				for _, a := range filter.Authors {
					pk, err := hex.DecodeString(a)
					if err != nil {
						return nil, err
					}
					queryParams = append(queryParams, pk)
				}
				query = append(query, "pubkey IN ("+strings.Repeat("?,", len(filter.Authors)-1)+"?)")
			}
			if len(filter.IDs) > 0 {
				for _, a := range filter.IDs {
					id, err := hex.DecodeString(a)
					if err != nil {
						return nil, err
					}
					queryParams = append(queryParams, id)
				}
				query = append(query, "id IN ("+strings.Repeat("?,", len(filter.IDs)-1)+"?)")
			}
			if len(query) == 0 {
				query = append(query, "1")
			}
			go func() {
				defer close(ch)
				q, err := db.QueryContext(ctx, "SELECT event FROM events WHERE "+strings.Join(query, " AND "), queryParams...)
				if err != nil {
					fmt.Println(err)
					return
				}
				var ctr = 0
				var scanctr = 0
				for {
					if !q.Next() {
						break
					}
					if scanctr >= 2000 {
						break
					}
					if (filter.Limit != 0 && ctr >= filter.Limit) || ctr >= 500 {
						break
					}
					var evt string
					err = q.Scan(&evt)
					if err != nil {
						fmt.Println(err)
						break
					}
					scanctr++
					nevt := nostr.Event{}
					easyjson.Unmarshal([]byte(evt), &nevt)
					if !filter.Matches(&nevt) {
						continue
					}
					ctr++
					ch <- &nevt
				}
			}()
			return ch, nil
		},
	)

	relay.RejectEvent = append(relay.RejectEvent,
		func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
			if !slices.Contains(acceptableEventKinds, event.Kind) {
				return true, "blocked: this relay only accepts kind 0/3"
			}
			bId, err := hex.DecodeString(event.ID)
			if err != nil {
				return true, "error: " + err.Error()
			}
			q := db.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", bId)
			if q.Err() != nil {
				return true, "error: " + err.Error()
			}
			var bogus int
			err = q.Scan(&bogus)
			if err == nil {
				return true, "duplicate:"
			} else if sql.ErrNoRows != err {
				return true, "error: " + err.Error()
			}
			return false, ""
		},
	)

	mux := relay.Router()
	mux.Handle("/", http.FileServer(http.Dir(("./frontend/"))))
	fmt.Println("running on :4834")
	http.ListenAndServe(":4834", relay)
}

func assert(err error) {
	if err != nil {
		panic(err)
	}
}
