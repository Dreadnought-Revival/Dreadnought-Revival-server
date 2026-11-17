#!/usr/bin/env bash
# wine-client.sh -- run the game client against this server, on this Linux box.
#
# This is the A/B test loop: change a response, restart mmogbrain, run this, read
# the log. It removes the "wait for the user to test on Windows" round trip that
# shaped most of this project's history.
#
#   bash scripts/wine-client.sh [seconds] [output-log]
#   SHOT_DIR=/somewhere bash scripts/wine-client.sh   # + PNGs of the running UI
#
# What it does, in order:
#   1. runs dn-launcher.exe with DN_USERNAME/DN_PASSWORD set (a test account's
#      sign-in, from run/secrets.env HARNESS_USERNAME/HARNESS_PASSWORD), which
#      authenticates against auth-server and DPAPI-writes the token into the
#      Wine registry, then kills the client the launcher spawns (we want our
#      own argv). The launcher must be 2026-11-17 or newer (run/dn-launcher.exe
#      from setup.sh, copied to $GAME_ROOT): the old DN_PLAYER_ID sign-in used
#      the Steam-ticket route, removed that day;
#   2. launches the client itself;
#   3. dismisses the title screen with xdotool;
#   4. lets it sit in the hangar for the requested time, then kills it.
#
# CPU is the thing to be careful about here: rendering is llvmpipe on the CPU,
# and an unconstrained run has taken this box down before. The client is pinned
# to three cores at nice 19 with a hard timeout, which has been safe on a 12-core
# host with the whole server stack running. Do not remove those.
#
# Requirements: Xvfb on :99, xdotool, a configured Wine prefix, and
# dn-launcher.json in the game's install root pointing at this server.
set -u

SECS="${1:-190}"
OUT="${2:-/tmp/dn-wine-client.log}"

: "${GAME_ROOT:=/root/projects/src/Dreadnought}"
: "${GAME_BIN_DIR:=$GAME_ROOT/DreadGame/DreadGame/Binaries/Win64}"
: "${WINEPREFIX:=/root/.wine}"
: "${DISPLAY:=:99}"
# The test account: create it once in the launcher (or POST /auth/register)
# and put its sign-in in run/secrets.env (gitignored), never in this file.
if [ -z "${DN_USERNAME:-}" ] && [ -f "$(dirname "$0")/../run/secrets.env" ]; then
    DN_USERNAME="$(grep '^HARNESS_USERNAME=' "$(dirname "$0")/../run/secrets.env" | cut -d= -f2- | tr -d '"')"
    DN_PASSWORD="$(grep '^HARNESS_PASSWORD=' "$(dirname "$0")/../run/secrets.env" | cut -d= -f2- | tr -d '"')"
fi
if [ -z "${DN_USERNAME:-}" ] || [ -z "${DN_PASSWORD:-}" ]; then
    echo "Set DN_USERNAME/DN_PASSWORD, or HARNESS_USERNAME/HARNESS_PASSWORD in run/secrets.env (a test account)." >&2
    exit 1
fi
: "${CLIENT_CPUS:=4,5,6}"

export WINEPREFIX DISPLAY DN_USERNAME DN_PASSWORD
export WINEDEBUG=-all
# No GPU here: the shipping build hard-requires DX11, which works through
# software GL. Without these it page-faults during RHI init. -opengl does not
# work, and -nullrhi boots but creates no window, so the title screen can never
# be dismissed.
export LIBGL_ALWAYS_SOFTWARE=1 GALLIUM_DRIVER=llvmpipe
export MESA_GL_VERSION_OVERRIDE=4.5 MESA_GLSL_VERSION_OVERRIDE=450

if ! pgrep -f "Xvfb $DISPLAY" >/dev/null 2>&1; then
    echo "Xvfb is not running on $DISPLAY -- start it first (scripts/start-services.sh does)." >&2
    exit 1
fi

kill_clients() {
    # By PID from the process table, never `pkill -f`: the pattern would match
    # this script's own command line and kill the shell running it. `pgrep -x`
    # does not work either -- the process name is truncated to 15 characters.
    for p in $(ps -eo pid,comm | awk '$2 ~ /^DreadGame/ {print $1}'); do
        kill -9 "$p" 2>/dev/null
    done
}

echo "[harness] minting a fresh auth token as $DN_USERNAME"
# NOT piped into grep. The launcher starts the game itself, and that child
# INHERITS the launcher's stdout -- so a pipe stays open long after `timeout`
# has killed the launcher, and the harness hangs here forever waiting on a grep
# that is really waiting on the game. (Observed: 10 minutes in phase 1 with the
# client still up and the real run never started.) Redirect to a file, then read
# the file: the child holding the fd open costs nothing.
LAUNCHER_LOG="${OUT%.log}.launcher.log"
(cd "$GAME_ROOT" && taskset -c "$CLIENT_CPUS" nice -n 19 timeout -s KILL 90 \
    wine ./dn-launcher.exe >"$LAUNCHER_LOG" 2>&1) || true
grep -E "Authenticated|token written|\[!\]" "$LAUNCHER_LOG" || \
    echo "[harness] launcher printed no auth line -- see $LAUNCHER_LOG"
sleep 2
kill_clients   # the launcher starts the game itself; we want our own argv
sleep 2

echo "[harness] launching the client for ${SECS}s -> $OUT"
cd "$GAME_BIN_DIR" || exit 1
taskset -c "$CLIENT_CPUS" nice -n 19 timeout -s KILL "$SECS" \
    wine ./DreadGame-Win64-Shipping.exe \
    -GatewayAddress=127.0.0.1 -GatewayPort=65443 \
    -YFirmamentAddress=127.0.0.1 -YFirmamentPort=48843 \
    -LOG -stdout -AllowStdOutLogVerbosity \
    -noeac -NoSteam -nosound -windowed -ResX=1024 -ResY=576 \
    "-LogCmds=global verbose, LogYComVOComponent log" -forcelogflush \
    >"$OUT" 2>&1 &
GAME=$!

# The client boots to a "press any key" title screen and will not talk to the
# servers until it gets input. Two windows exist; the one to drive is named
# "DreadGame" and the other carries the full exe path, so exclude any name with
# a backslash in it.
WID=""
for _ in $(seq 1 60); do
    sleep 2
    WID=$(xdotool search --name "^DreadGame" 2>/dev/null | while read -r w; do
            n=$(xdotool getwindowname "$w" 2>/dev/null)
            case "$n" in *\\*) ;; *) echo "$w"; break ;; esac
          done)
    [ -n "$WID" ] && break
done
if [ -n "$WID" ]; then
    sleep 25   # let it finish loading before the keypress
    xdotool windowactivate "$WID" windowfocus "$WID" 2>/dev/null
    xdotool key --window "$WID" space 2>/dev/null
    sleep 2
    xdotool key --window "$WID" Return 2>/dev/null
    echo "[harness] dismissed the title screen (window $WID)"
else
    echo "[harness] no game window appeared -- check $OUT" >&2
fi

# Screenshots. The whole history of this project is arguing about client state
# from log lines; a PNG of the hangar settles questions the log cannot answer at
# all (does the credit counter show a number, is the tech tree preview empty).
# Taken from the X server rather than the window, because the client's window is
# borderless and `import -window` on it has come back black.
shoot() {
    [ -n "${SHOT_DIR:-}" ] || return 0
    mkdir -p "$SHOT_DIR"
    local name="$SHOT_DIR/$1.png"
    if command -v import >/dev/null 2>&1; then
        import -display "$DISPLAY" -window root "$name" 2>/dev/null && echo "[harness] shot: $name"
    fi
}

if [ -n "${SHOT_DIR:-}" ]; then
    # One per phase, so a run that never reaches the hangar still shows where it
    # stopped rather than leaving one black frame.
    (   sleep 60;  shoot 01-after-keypress
        sleep 45;  shoot 02-hangar
        sleep 45;  shoot 03-hangar-late ) &
fi

wait $GAME 2>/dev/null
kill_clients
echo "[harness] done. $(grep -c . "$OUT") lines in $OUT"
echo "[harness] the client writes NO log file of its own under Wine; stdout is all there is,"
echo "[harness] and it is capped at Log verbosity even with -LogCmds."
