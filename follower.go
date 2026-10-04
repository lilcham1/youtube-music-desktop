package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"youtube-music/internal/discordbot"
	"youtube-music/internal/lastfm"
	"youtube-music/internal/secret"
	"youtube-music/internal/settings"
	"youtube-music/internal/ytmusic"
)

const (
	lastfmFollowPoll = 10 * time.Second
	// followLatency is added to a Discord listener's position: the time for
	// their status to reach us and for YouTube Music to start the song.
	followLatency = 1500 * time.Millisecond
)

// follower plays along with someone listening on Spotify, seen either
// through the user's Discord bot (exact song and position, live) or
// through Last.fm (the current song, checked every few seconds).
type follower struct {
	d       *desktop
	pushUI  *debouncer
	applyMu sync.Mutex // one apply at a time, in order

	mu        sync.Mutex
	bot       *discordbot.Client
	botCancel context.CancelFunc
	mode      string // "", "discord" or "lastfm"
	target    string // Discord user ID or Last.fm username
	name      string
	cancel    context.CancelFunc
	status    string
	following string
	lastKey   string
	lastVideo string
	resolved  map[string]string // track key → video ID ("" = not found)
}

func newFollower(d *desktop) *follower {
	f := &follower{d: d, resolved: map[string]string{}}
	f.pushUI = newDebouncer(300*time.Millisecond, d.pushAllState)
	return f
}

// startBot connects the saved Discord bot, replacing any running one.
func (f *follower) startBot() {
	f.mu.Lock()
	if f.botCancel != nil {
		f.botCancel()
		f.botCancel, f.bot = nil, nil
	}
	f.mu.Unlock()
	token, err := secret.Unprotect(f.d.config().DiscordBot.Token)
	if err != nil || token == "" {
		f.pushUI.Trigger()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	bot := &discordbot.Client{Token: token, URL: devOverride("YTM_DISCORD_GATEWAY")}
	bot.OnChange = func(userID string) {
		f.pushUI.Trigger()
		f.mu.Lock()
		mine := f.mode == "discord" && userID == f.target && f.bot == bot
		f.mu.Unlock()
		if mine {
			go f.applyDiscord(bot)
		}
	}
	f.mu.Lock()
	f.bot, f.botCancel = bot, cancel
	f.mu.Unlock()
	go func() {
		if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("discord bot: %v", err)
			f.pushUI.Trigger()
		}
	}()
}

func (f *follower) saveBotToken(token string) {
	token = strings.TrimSpace(token)
	protected := ""
	if token != "" {
		var err error
		if protected, err = secret.Protect(token); err != nil {
			f.setStatus("Couldn't store the bot token securely: " + err.Error())
			return
		}
	}
	f.d.update(func(c *settings.Settings) { c.DiscordBot.Token = protected })
	f.stop("")
	f.startBot()
}

func (f *follower) state() map[string]any {
	f.mu.Lock()
	bot := f.bot
	out := map[string]any{"mode": f.mode, "target": f.target, "name": f.name, "status": f.status, "following": f.following}
	f.mu.Unlock()
	cfg := f.d.config()
	out["botConfigured"] = cfg.DiscordBot.Token != ""
	out["lastfmKey"] = lastfmReadKey(cfg.LastFM) != ""
	if bot != nil {
		st := bot.Status()
		out["botConnected"] = st.Connected
		out["botName"] = st.BotName
		out["botServers"] = st.Servers
		if st.BotID != "" {
			out["inviteUrl"] = discordbot.InviteURL(st.BotID)
		}
		if st.Err != nil && !st.Connected {
			out["botError"] = sentence(st.Err)
		}
		var listeners []map[string]any
		for _, l := range bot.Listening() {
			listeners = append(listeners, map[string]any{"id": l.UserID, "name": l.Name, "title": l.Spotify.Title, "artist": strings.Join(l.Spotify.Artists, ", ")})
		}
		out["listeners"] = listeners
	}
	return out
}

func (f *follower) setStatus(status string) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
	f.pushUI.Trigger()
}

// begin switches to following target; other modes that drive the player
// stop first.
func (f *follower) begin(mode, target, name string) context.Context {
	f.stop("")
	f.d.listen.leaveAsGuest()
	f.d.queue.stopIfActive("Stopped to follow " + name + ".")
	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	f.mode, f.target, f.name, f.cancel = mode, target, name, cancel
	f.lastKey, f.lastVideo, f.following = "", "", ""
	f.mu.Unlock()
	log.Printf("follow: %s via %s", name, mode)
	return ctx
}

// FollowDiscord follows someone the bot can see.
func (f *follower) FollowDiscord(userID string) {
	f.mu.Lock()
	bot := f.bot
	f.mu.Unlock()
	if bot == nil {
		f.setStatus("Connect your Discord bot first.")
		return
	}
	l := bot.Get(userID)
	f.begin("discord", userID, l.Name)
	f.setStatus("Following " + l.Name + ".")
	f.applyDiscord(bot)
}

// applyDiscord mirrors the followed person's current Spotify state.
func (f *follower) applyDiscord(bot *discordbot.Client) {
	f.applyMu.Lock()
	defer f.applyMu.Unlock()
	f.mu.Lock()
	if f.mode != "discord" || f.bot != bot {
		f.mu.Unlock()
		return
	}
	userID, name, lastVideo := f.target, f.name, f.lastVideo
	f.mu.Unlock()

	l := bot.Get(userID)
	if l.Spotify == nil {
		if lastVideo != "" {
			f.d.command("pause", nil)
		}
		f.setStatus(name + " isn't playing anything on Spotify right now. You'll follow along when they start.")
		return
	}
	s := l.Spotify
	track := ytmusic.Track{Title: s.Title, Artists: s.Artists, Duration: s.Duration()}
	video := f.resolve("spotify:"+s.TrackID, track)
	label := s.Title + " — " + strings.Join(s.Artists, ", ")
	if video == "" {
		f.d.command("pause", nil)
		f.mu.Lock()
		f.following = ""
		f.mu.Unlock()
		f.setStatus("“" + label + "” isn't on YouTube Music. Paused until " + name + "'s next song.")
		return
	}
	position := (s.Position(time.Now()) + followLatency).Seconds()
	f.d.command("follow", map[string]any{"videoId": video, "positionSeconds": position, "playing": true})
	f.mu.Lock()
	f.lastKey, f.lastVideo, f.following = s.TrackID, video, label
	f.mu.Unlock()
	f.setStatus("Following " + name + " on Spotify.")
}

// FollowLastfm follows a Last.fm user's now-playing track.
func (f *follower) FollowLastfm(user string) {
	user = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(user), "@"))
	if i := strings.LastIndex(user, "/user/"); i >= 0 { // a profile link
		user = strings.Trim(user[i+len("/user/"):], "/")
	}
	if user == "" {
		f.setStatus("Enter a Last.fm username.")
		return
	}
	apiKey := lastfmReadKey(f.d.config().LastFM)
	if apiKey == "" {
		f.setStatus("Add a Last.fm API key in the Last.fm tab first (no sign-in needed to follow someone).")
		return
	}
	client := &lastfm.Client{APIKey: apiKey, APIURL: devOverride("YTM_LASTFM_API")}
	ctx := f.begin("lastfm", user, user)
	f.setStatus("Checking what " + user + " is playing…")
	go func() {
		for {
			f.pollLastfm(ctx, client, user)
			select {
			case <-ctx.Done():
				return
			case <-time.After(lastfmFollowPoll):
			}
		}
	}()
}

func (f *follower) pollLastfm(ctx context.Context, client *lastfm.Client, user string) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	playing, err := client.Listening(reqCtx, user)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if errors.Is(err, lastfm.ErrUnknownUser) {
			f.stop(sentence(err))
			return
		}
		f.setStatus("Couldn't reach Last.fm (" + err.Error() + "). Trying again…")
		return
	}
	if playing == nil {
		f.setStatus(user + " isn't playing anything on Last.fm right now. You'll follow along when they start.")
		return
	}
	f.mu.Lock()
	same := f.mode == "lastfm" && f.lastKey == playing.Key()
	f.mu.Unlock()
	if same {
		return
	}
	label := playing.Title + " — " + playing.Artist
	video := f.resolve("lastfm:"+playing.Key(), ytmusic.Track{Title: playing.Title, Artists: []string{playing.Artist}})
	f.mu.Lock()
	if f.mode != "lastfm" || f.target != user {
		f.mu.Unlock()
		return
	}
	f.lastKey = playing.Key()
	f.mu.Unlock()
	if video == "" {
		f.setStatus("“" + label + "” isn't on YouTube Music. Waiting for " + user + "'s next song.")
		return
	}
	f.d.command("follow", map[string]any{"videoId": video, "positionSeconds": 0, "playing": true})
	f.mu.Lock()
	f.lastVideo, f.following = video, label
	f.mu.Unlock()
	f.setStatus(fmt.Sprintf("Following %s on Last.fm (song changes show up within about %d seconds).", user, int(lastfmFollowPoll.Seconds())))
}

// devOverride reads a test endpoint from the environment in development
// builds only; release builds always use the real services.
func devOverride(name string) string {
	if !strings.HasSuffix(version, "-dev") {
		return ""
	}
	return os.Getenv(name)
}

// resolve finds the YouTube Music song for a track, remembering results.
func (f *follower) resolve(key string, track ytmusic.Track) string {
	f.mu.Lock()
	video, known := f.resolved[key]
	f.mu.Unlock()
	if known {
		return video
	}
	f.d.mu.Lock()
	yt := &ytmusic.Client{ClientVersion: f.d.ytVersion}
	f.d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	songs, err := yt.SearchSongs(ctx, track.Query())
	if err != nil {
		log.Printf("follow: search %q: %v", track.Query(), err)
		return "" // not cached: try again next time
	}
	if song, ok := ytmusic.BestMatch(track, songs); ok {
		video = song.VideoID
	}
	f.mu.Lock()
	f.resolved[key] = video
	f.mu.Unlock()
	return video
}

// stop ends following; status replaces the message when not empty.
func (f *follower) stop(status string) {
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	wasFollowing := f.mode != ""
	f.mode, f.target, f.name, f.following, f.lastKey, f.lastVideo = "", "", "", "", "", ""
	if status != "" || wasFollowing {
		f.status = status
	}
	f.mu.Unlock()
	if wasFollowing {
		log.Print("follow: stopped")
	}
	f.pushUI.Trigger()
}

// stopIfActive ends following without touching the status otherwise.
func (f *follower) stopIfActive(status string) {
	f.mu.Lock()
	active := f.mode != ""
	f.mu.Unlock()
	if active {
		f.stop(status)
	}
}
