# systemd deployment

## Install

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin kemp_exporter

sudo install -o root -g root -m 0755 bin/kemp_exporter /usr/local/bin/kemp_exporter

sudo mkdir -p /etc/kemp_exporter
sudo install -o root -g kemp_exporter -m 0640 config.yaml /etc/kemp_exporter/config.yaml
sudo install -o root -g kemp_exporter -m 0600 deploy/kemp_exporter.env.example /etc/kemp_exporter/kemp_exporter.env
sudo $EDITOR /etc/kemp_exporter/kemp_exporter.env   # set KEMP1_HOSTNAME / KEMP1_APIKEY

sudo install -o root -g root -m 0644 deploy/kemp_exporter.service /etc/systemd/system/kemp_exporter.service
sudo systemctl daemon-reload
sudo systemctl enable --now kemp_exporter
```

`config.yaml` is read directly (not templated) — secrets referenced as `${VAR}`
inside it are resolved at load time from the process environment, which
`EnvironmentFile=/etc/kemp_exporter/kemp_exporter.env` populates.

## Operate

```bash
systemctl status kemp_exporter
journalctl -u kemp_exporter -f

# Hot reload the target list/credentials without dropping a collection cycle:
sudo systemctl reload kemp_exporter
```

`ExecReload=/bin/kill -HUP $MAINPID` in the unit maps `systemctl reload` onto the
exporter's built-in SIGHUP hot reload (see
[ADR 0008](../adr/0008-config-hot-reload.md)) — no restart, no scrape gap. The
config directory is also file-watched, so an in-place edit picks up automatically
even without an explicit `reload`.

Curl the metrics endpoint locally to confirm it's serving:

```bash
curl -s http://localhost:9447/metrics | grep kemp_exporter_build_info
```

## Harden

`deploy/kemp_exporter.service` already applies a restrictive sandbox by default:

- Runs as the dedicated, unprivileged `kemp_exporter` system user/group (no login
  shell, no home directory).
- `NoNewPrivileges=true`, `ProtectSystem=strict`, `ProtectHome=true`,
  `PrivateTmp=true`, `PrivateDevices=true`.
- `ProtectKernelTunables=true`, `ProtectKernelModules=true`,
  `ProtectControlGroups=true`.
- `RestrictAddressFamilies=AF_INET AF_INET6` (no Unix sockets, no exotic address
  families — the exporter only makes outbound HTTPS calls and serves one HTTP port).
- `RestrictNamespaces=true`, `RestrictRealtime=true`, `LockPersonality=true`,
  `MemoryDenyWriteExecute=true`.
- `Restart=on-failure` with a 10s backoff.

The env file (`/etc/kemp_exporter/kemp_exporter.env`) should be `0600`, owned
`root:kemp_exporter` (or equivalent), so only the exporter's own group can read the
credentials it contains — never world-readable.

## macOS note

**`brew services` is not wired up for this project.** The published Homebrew cask
(`brew install --cask fjacquet/tap/kemp_exporter`) only drops the `kemp_exporter`
binary onto disk; there is no `service` block in the cask, so `brew services start
kemp_exporter` will not find anything to manage. If you need this running as a
background service on macOS, register a `launchd` job by hand — for example:

```xml
<!-- ~/Library/LaunchAgents/com.fjacquet.kemp_exporter.plist -->
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.fjacquet.kemp_exporter</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/kemp_exporter</string>
    <string>--config</string>
    <string>/usr/local/etc/kemp_exporter/config.yaml</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>KEMP1_HOSTNAME</key><string>lm-prod-01.example.com</string>
    <key>KEMP1_APIKEY</key><string>your-read-only-api-key</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/usr/local/var/log/kemp_exporter.log</string>
  <key>StandardErrorPath</key><string>/usr/local/var/log/kemp_exporter.err.log</string>
</dict>
</plist>
```

```bash
launchctl load -w ~/Library/LaunchAgents/com.fjacquet.kemp_exporter.plist
```

`SIGHUP`-based reload still works under `launchd`:
`launchctl kill HUP com.fjacquet.kemp_exporter` (or the running PID directly).
