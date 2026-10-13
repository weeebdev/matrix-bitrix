package bitrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

// PullEvent is a single push-server event, e.g. module_id=im, command=message.
type PullEvent struct {
	ModuleID string          `json:"module_id"`
	Command  string          `json:"command"`
	Params   json.RawMessage `json:"params"`
	Extra    json.RawMessage `json:"extra"`
}

type pullEnvelope struct {
	ID      json.RawMessage `json:"id"`
	MID     string          `json:"mid"`
	Channel string          `json:"channel"`
	Tag     string          `json:"tag"`
	Time    string          `json:"time"`
	Text    PullEvent       `json:"text"`
}

// websocket close codes used by the Bitrix push server
const (
	closeChannelExpired  = 3001
	closeConfigExpired   = 3003
	closeWrongChannelID  = 4010
	pullRevision         = "19"
	pullPingInterval     = 30 * time.Second
	pullDedupeBufferSize = 200
)

var reNginxMessage = regexp.MustCompile(`(?s)#!NGINXNMS!#(.*?)#!NGINXNME!#`)

type PullClient struct {
	client  *Client
	log     zerolog.Logger
	handler func(ctx context.Context, evt *PullEvent)
	// OnConnect/OnDisconnect are called on connection state changes.
	OnConnect    func()
	OnDisconnect func(err error)

	config  *PullConfig
	mid     string
	tag     string
	time    string
	seenMID []string
}

func (c *Client) NewPullClient(log zerolog.Logger, handler func(ctx context.Context, evt *PullEvent)) *PullClient {
	return &PullClient{client: c, log: log, handler: handler}
}

// Run connects and keeps the connection alive until ctx is cancelled.
func (pc *PullClient) Run(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		err := pc.connectOnce(ctx, func() { attempt = 0 })
		if ctx.Err() != nil {
			return
		}
		if pc.OnDisconnect != nil {
			pc.OnDisconnect(err)
		}
		attempt++
		delay := time.Duration(min(attempt*attempt, 60)) * time.Second
		pc.log.Warn().Err(err).Dur("retry_in", delay).Msg("Push connection lost")
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

func (pc *PullClient) configExpired() bool {
	if pc.config == nil {
		return true
	}
	for _, ch := range pc.config.Channels {
		if !ch.End.IsZero() && time.Until(ch.End.Time) < time.Minute {
			return true
		}
	}
	return false
}

func (pc *PullClient) connectURL() (string, error) {
	cfg := pc.config
	base := cfg.Server.WebSocketSecure
	if base == "" {
		base = cfg.Server.WebSocket
	}
	if base == "" || !cfg.Server.WebSocketEnable {
		return "", errors.New("websocket push is not enabled on this portal")
	}
	if strings.HasPrefix(base, "/") {
		u := *pc.client.BaseURL
		u.Scheme = strings.Replace(strings.Replace(u.Scheme, "https", "wss", 1), "http", "ws", 1)
		base = u.String() + base
	}
	q := url.Values{}
	if cfg.JWT != "" {
		q.Set("token", cfg.JWT)
	} else {
		var ids []string
		for _, typ := range []string{"private", "shared"} {
			if ch, ok := cfg.Channels[typ]; ok && ch.ID != "" {
				ids = append(ids, ch.ID)
			}
		}
		if len(ids) == 0 {
			return "", errors.New("pull config has no channels")
		}
		q.Set("CHANNEL_ID", strings.Join(ids, "/"))
	}
	if cfg.Server.Mode == "shared" && cfg.ClientID != "" {
		q.Set("clientId", cfg.ClientID)
	}
	if pc.mid != "" {
		q.Set("mid", pc.mid)
	}
	if pc.tag != "" {
		q.Set("tag", pc.tag)
	}
	if pc.time != "" {
		q.Set("time", pc.time)
	}
	q.Set("revision", pullRevision)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + q.Encode(), nil
}

func (pc *PullClient) connectOnce(ctx context.Context, onOpen func()) error {
	if pc.configExpired() {
		cfg, err := pc.client.GetPullConfig(ctx)
		if err != nil {
			return fmt.Errorf("failed to get pull config: %w", err)
		}
		pc.config = cfg
	}
	chans := zerolog.Dict()
	for typ, ch := range pc.config.Channels {
		chans.Str(typ, strings.SplitN(ch.ID, ".", 2)[0])
	}
	pc.log.Debug().Dict("channels", chans).Str("mode", pc.config.Server.Mode).Int("server_version", pc.config.Server.Version).Bool("jwt", pc.config.JWT != "").Msg("Using push config")
	wsURL, err := pc.connectURL()
	if err != nil {
		return err
	}
	header := http.Header{}
	header.Set("User-Agent", userAgent)
	header.Set("Origin", pc.client.BaseURL.String())
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{Jar: pc.client.HTTP.Jar, Timeout: 30 * time.Second},
		HTTPHeader: header,
	})
	if err != nil {
		pc.config = nil
		return fmt.Errorf("failed to dial push server: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	defer conn.CloseNow()
	pc.log.Info().Msg("Connected to push server")
	onOpen()
	if pc.OnConnect != nil {
		pc.OnConnect()
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(pullPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(connCtx, 15*time.Second)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil && connCtx.Err() == nil {
					pc.log.Warn().Err(err).Msg("Push server ping failed")
					_ = conn.Close(websocket.StatusGoingAway, "ping timeout")
					return
				}
			}
		}
	}()

	for {
		// No read deadline: an expiring read context closes the whole connection, and quiet
		// periods are normal. Liveness is checked by the ping loop instead.
		_, data, err := conn.Read(connCtx)
		if err != nil {
			switch websocket.CloseStatus(err) {
			case closeChannelExpired, closeConfigExpired, closeWrongChannelID:
				pc.config = nil
			}
			return err
		}
		if restart := pc.handleFrame(ctx, data); restart {
			pc.config = nil
			_ = conn.Close(websocket.StatusNormalClosure, "config expired")
			return errors.New("push server requested reconnect")
		}
	}
}

func (pc *PullClient) seen(mid string) bool {
	for _, m := range pc.seenMID {
		if m == mid {
			return true
		}
	}
	pc.seenMID = append(pc.seenMID, mid)
	if len(pc.seenMID) > pullDedupeBufferSize {
		pc.seenMID = pc.seenMID[len(pc.seenMID)-pullDedupeBufferSize:]
	}
	return false
}

func (pc *PullClient) handleFrame(ctx context.Context, data []byte) (restart bool) {
	matches := reNginxMessage.FindAllSubmatch(data, -1)
	if matches == nil {
		pc.log.Trace().Str("data", string(data)).Msg("Ignoring non-message push frame")
		return false
	}
	for _, m := range matches {
		var env pullEnvelope
		if err := json.Unmarshal(m[1], &env); err != nil {
			pc.log.Warn().Err(err).Str("data", string(m[1])).Msg("Failed to parse push message")
			continue
		}
		pc.mid, pc.tag, pc.time = env.MID, env.Tag, env.Time
		if env.MID != "" && pc.seen(env.MID) {
			continue
		}
		evt := &env.Text
		evt.ModuleID = strings.ToLower(evt.ModuleID)
		pc.log.Trace().
			Str("module", evt.ModuleID).
			Str("command", evt.Command).
			RawJSON("params", nonEmptyJSON(evt.Params)).
			Msg("Push event")
		if evt.ModuleID == "pull" {
			switch strings.ToUpper(evt.Command) {
			case "CHANNEL_EXPIRE", "CONFIG_EXPIRE", "SERVER_RESTART":
				restart = true
			}
			continue
		}
		pc.handler(ctx, evt)
	}
	return restart
}

func nonEmptyJSON(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage("null")
	}
	return data
}
