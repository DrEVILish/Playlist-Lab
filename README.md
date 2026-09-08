## Playlist Lab

Feel free to donate
[Tip Jar](https://www.paypal.com/donate/?business=6H5L2S8SAQWBW&no_recurring=0&currency_code=AUD)

A comprehensive music playlist management system with Plex Media Server integration. Import playlists from multiple sources, generate smart mixes, and sync them to your Plex server.

## Features

### Core Features
- **Multi-Source Import**: Import playlists from Spotify, Apple Music, Tidal, Qobuz, Amazon Music, Deezer, ListenBrainz, YouTube Music, and chart sources (ARIA, Billboard)
- **Import Review**: Preview matched/unmatched tracks, manually re-match individual tracks, then confirm before a playlist is created
- **Smart Playlist Generation**: Create dynamic playlists based on genres, moods, and listening patterns
- **AI Playlist Generation**: Describe a playlist in plain language via Gemini or Grok
- **Plex Integration**: Seamless sync with Plex Media Server
- **Multi-User Support**: Manage playlists for multiple Plex users and share between them
- **Playlist Sharing**: Share playlists between Plex Home users
- **Scheduling**: Automatically update playlists on a schedule
- **Missing Track Detection**: Identify missing tracks, with optional Deemix/Lidarr acquisition

### Playlist Editing
- **Shuffle, Sort, Dedupe, Split, Rename**: Full in-place playlist editing
- **Cover Upload**: Set a custom playlist cover image
- **Export**: Export any playlist to M3U, M3U8, PLS, XSPF, CSV, or TXT
- **Search & Add**: Search your Plex library and add tracks directly

### Admin
- **User Management**: Enable/disable/promote/delete users
- **Deemix / Lidarr / YouTube OAuth**: Configure missing-track acquisition and YouTube import credentials from the admin panel, no server restart required
- **Log Viewer**: Browse and filter server logs
- **Schedules**: View and manage every user's schedules in one place

## Technology Stack

- **Backend**: Go, chi router, `html/template`, SQLite (modernc.org/sqlite, no cgo)
- **Frontend**: HTMX, server-rendered templates - no separate frontend build or JS framework
- **Browser scraping**: chromedp (headless Chromium) for sources with no public playlist-read API (Apple Music, Tidal, Qobuz, Amazon Music, ARIA charts)

## Quick Start

### Prerequisites
- Go 1.26+
- A Plex Media Server with an authentication token
- (Optional) A system Chromium/Chrome binary, for the browser-scraping import sources

### Build and Run
```bash
git clone https://github.com/AuXBoX/playlist-lab.git
cd playlist-lab
go build -o bin/playlist-lab-server ./cmd/server
SESSION_SECRET=$(openssl rand -hex 32) ./bin/playlist-lab-server
# Serves on http://localhost:3001
```

See `.env.example` for the full list of environment variables (database path, session secret, OAuth credentials, Deemix/Lidarr config, etc.) - copy it to `.env` and the binary will pick it up via the systemd unit's `EnvironmentFile`, or export the variables directly.

### Running as a systemd service
```ini
[Unit]
Description=Playlist Lab Server
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/playlist-lab-server
EnvironmentFile=/opt/playlist-lab-server/.env
ExecStart=/opt/playlist-lab-server/bin/playlist-lab-server
Restart=on-failure
RestartSec=10
Environment=NODE_ENV=production
Environment=PORT=3001

[Install]
WantedBy=multi-user.target
```

### Reverse Proxy Support

Playlist Lab works seamlessly behind reverse proxies (Nginx, Apache, Caddy, etc.) - see `deployment/nginx.conf`, `deployment/apache.conf`, and `deployment/caddy.conf` for working examples.

**Example Nginx Configuration:**
```nginx
location / {
  proxy_pass http://localhost:3001;
  proxy_set_header Host $host;
  proxy_set_header X-Real-IP $remote_addr;
  proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
  proxy_set_header X-Forwarded-Proto $scheme;
}
```

This ensures OAuth callbacks and other features work correctly with your reverse proxy setup.

## License

See LICENSE file for details.

## Acknowledgments

- Plex Media Server for the excellent media platform
- All the open-source libraries that make this possible

## 📧 Support

- Issues: [GitHub Issues](https://github.com/AuXBoX/playlist-lab/issues)
- Community: [r/PlaylistLab](https://www.reddit.com/r/PlaylistLab/)
