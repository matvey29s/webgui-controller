# PLC Controller — Local Web GUI

Self-contained local web UI for the AM3352-based modular controller.
Backend is **pure Go standard library** (`net/http` only — no external
Go modules, nothing to fetch from a module proxy, single static binary).

## How it fits together

The diagram in `public/index.html` is your original `Web_GUI.svg`,
byte-for-byte unchanged — same paths, same colors, same positions. The only
edits are added `id`/`class`/`tabindex` attributes on each port's `<g>` so
`app.js` can address them; a script-verified pixel diff against the source
file came back empty (0 changed pixels) before those attributes were added.

Port ids match the actual labels drawn in the artwork: `power`,
`ethernet1`, `ethernet2`, `usb`, `audioIn`, `audioOut`, `sdcard`, `lcd`,
`modules`. Of these, `modules` (the expansion bay) is the only one with
ADC-based type identification and a right-click RS-485 config panel; the
rest are plain connected/present indicators.

State toggling never draws anything new: every port except the LCD bar
already has, in your original file, a solid colored shape plus a sibling
white outline path. Going "inactive" just clears the colored shape's fill
(`element.style.fill = 'none'`), which reveals that existing white outline
against the green chassis — precisely the "empty rectangle, white border"
look you described. The LCD bar has no separate outline in the source, so
its inactive state adds a stroke directly instead of just clearing the fill
(see the comment in `style.css`/`app.js`).

```
public/                served as static files by the Go binary
  index.html           the exact SVG artwork, tagged with ids only
  style.css            state = toggling the artwork's existing layers
  app.js               state store + SSE client + tooltip + config modal

server/
  main.go              HTTP routes + Server-Sent Events hub
  adc.go               hardware detection (mocked — see comments for the
                        real AM3352 IIO/ADC + eth0/eth1 + mmc + USB wiring)
  config.go            RS-485 config validation + JSON persistence
  modules-config.json  per-port hover text (title/description/what to connect)
```

**Why SSE instead of WebSocket:** with only `net/http` and no external
modules allowed, Server-Sent Events give one-way server->browser push
using plain HTTP chunked responses — no protocol upgrade/handshake code
to hand-roll. The browser's built-in `EventSource` handles reconnection
automatically. Config changes go back to the server as ordinary REST
`POST` requests, so the whole thing only ever needs plain HTTP.

**State flow:** `AdcMonitor` polls (simulated) hardware every 500ms,
diffs against the last known state, and emits only what changed onto a
channel. `main.go` fans that out to every connected browser over
`/api/events`. The frontend never polls — it renders whatever state it's
handed and applies CSS classes; there's no separate rendering engine.

## Wiring to real hardware

Everything to change lives in `server/adc.go`, clearly marked:
1. `readBayVoltage()` (the "modules" expansion bay) → read
   `/sys/bus/iio/devices/iio:device0/in_voltage<N>_raw`
2. `ethernet1` / `ethernet2` → read `/sys/class/net/eth0/carrier`,
   `.../eth1/carrier`
3. `sdcard` → `os.Stat` your SD block device (e.g. `/dev/mmcblk1`)
4. `usb` / `audioIn` / `audioOut` / `power` / `lcd` → whatever presence/GPIO
   line your board exposes for each
5. `idTable` → recalibrate the bay's voltage bands against your real ID
   resistors

Nothing in `main.go` or the frontend needs to change — they only see the
resolved `SlotState` values.

The RS-485 config POST handler in `main.go` has a marked spot to push
saved settings into your real serial driver (termios on `/dev/ttyS*`, or
an IPC call to a separate fieldbus daemon).

## Build & run

```sh
cd server
go build -o plc-webgui .
./plc-webgui                      # listens on :80 by default
# or:
PLC_WEBGUI_ADDR=:8080 ./plc-webgui
```

Then, from a PC connected via Ethernet to the controller, browse to
`http://<controller-ip>/`.

To cross-compile for the AM3352 target (ARMv7, hard-float) from a dev
machine:

```sh
GOOS=linux GOARCH=arm GOARM=7 go build -o plc-webgui .
```

Copy the resulting binary plus the `public/` and
`server/modules-config.json` alongside it onto the controller (paths in
`main.go` are relative — adjust `publicDir`/`metadataPath` constants if
you deploy with a different layout, e.g. under `/opt/plc-webgui/`).

## Demo mode

With no hardware attached, `adc.go` simulates an RS-485 interface plugged
into the "modules" bay, Ethernet 1 linked, and an SD card inserted; the
other ports start disconnected. The "Demo: simulate hardware" panel at the
bottom of the page lets you plug/unplug the bay (RS-485 or relay), drop the
Ethernet link, and insert/eject USB and SD from the browser, so you can see
the diagram, tooltips and config modal react live. Remove `SimulateHotplug`
and the `/api/_simulate/` route once real hardware reads are in place.

## Interaction summary

- **Hover** any port -> popup with title, description, what it should be
  connected to, and live status (connected / not connected, or detected
  type / nothing detected for the bay).
- **Right-click** `modules` -> opens the RS-485 config modal (baud / parity
  / stop bits / data bits) *only when an RS-485 interface is currently
  detected in the bay*; otherwise nothing opens. Saves are re-validated on
  the server, which rejects them (409) if the interface was unplugged.
- Port highlighting is fully automatic: solid color = connected/detected,
  white outline only = not connected, driven by the (simulated, then real)
  hardware reads.

## Note on module1/module2/interface1/interface2

Your supplied artwork draws a single "Modules" tile, not four separate
slots, so this build models the bay as one port. If you have artwork for
the individual slots, each just needs the same id/class tagging and one
more entry in `adc.go` and `modules-config.json`.
