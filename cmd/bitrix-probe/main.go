// bitrix-probe is a read-only smoke test for the Bitrix client, without Matrix.
// It logs in, lists recent chats, loads a few messages and prints raw push events.
//
//	BITRIX_URL=https://portal.example.com BITRIX_LOGIN=user BITRIX_PASSWORD=... go run ./cmd/bitrix-probe -listen 2m
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/rs/zerolog"

	"github.com/weeebdev/matrix-bitrix/pkg/bitrix"
)

func main() {
	listen := flag.Duration("listen", time.Minute, "how long to listen for push events")
	chats := flag.Int("chats", 5, "recent chats to list")
	flag.Parse()

	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}).With().Timestamp().Logger().Level(zerolog.TraceLevel)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	c, err := bitrix.NewClient(os.Getenv("BITRIX_URL"))
	if err != nil {
		log.Fatal().Err(err).Msg("bad URL")
	}
	if err = c.PasswordLogin(ctx, os.Getenv("BITRIX_LOGIN"), os.Getenv("BITRIX_PASSWORD")); err != nil {
		log.Fatal().Err(err).Msg("login failed")
	}
	log.Info().Int64("user_id", c.UserID).Str("site", c.SiteID).Msg("Logged in")

	recent, err := c.RecentList(ctx, *chats, "")
	if err != nil {
		log.Fatal().Err(err).Msg("recent list failed")
	}
	for _, it := range recent.Items {
		log.Info().Str("dialog", string(it.ID)).Int64("chat_id", int64(it.ChatID)).Str("type", it.Type).Str("title", it.Title).Msg("Recent chat")
	}
	if len(recent.Items) > 0 {
		dialog := string(recent.Items[0].ID)
		batch, err := c.LoadChat(ctx, dialog, 3)
		if err != nil {
			log.Err(err).Msg("load chat failed")
		} else {
			log.Info().Str("dialog", dialog).Int64("chat_id", int64(batch.Chat.ID)).Str("type", batch.Chat.Type).Int("messages", len(batch.Messages)).Msg("Loaded chat")
			for _, m := range batch.Messages {
				log.Info().Int64("id", int64(m.ID)).Int64("author", m.Sender()).Ints64("files", m.FileIDs()).Int64("reply", m.ReplyID()).Str("text", m.Text).Msg("Message")
			}
			if tail, err := c.MessageTail(ctx, int64(batch.Chat.ID), 0, 3, true); err != nil {
				log.Err(err).Msg("message tail failed")
			} else {
				log.Info().Int("messages", len(tail.Messages)).Msg("Message.tail OK")
			}
		}
		if users, err := c.DialogUsers(ctx, dialog); err != nil {
			log.Err(err).Msg("dialog users failed")
		} else {
			log.Info().Int("count", len(users)).Msg("Dialog users OK")
		}
	}

	listenCtx, stop := context.WithTimeout(ctx, *listen)
	defer stop()
	pull := c.NewPullClient(log.With().Str("component", "pull").Logger(), func(ctx context.Context, evt *bitrix.PullEvent) {
		pretty, _ := json.MarshalIndent(json.RawMessage(evt.Params), "", "  ")
		fmt.Printf("=== %s.%s\n%s\n", evt.ModuleID, evt.Command, pretty)
	})
	log.Info().Dur("for", *listen).Msg("Listening for push events")
	pull.Run(listenCtx)
}
