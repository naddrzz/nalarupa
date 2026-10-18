#!/bin/bash
# wl-run CMD...: run CMD inside a private headless Wayland desktop (sway, CPU rendering, 1920x1080).
# GPUI's Linux build has no X11 backend (so no Xvfb) and needs a wl_seat (weston headless has none).
# Needs: sway, grim (screenshots), mesa-vulkan-drivers (lavapipe). Example: scripts/wl-run.sh bun run e2e
export XDG_RUNTIME_DIR=$(mktemp -d /tmp/wl.XXXXXX)
unset DISPLAY WAYLAND_DISPLAY
cfg="$XDG_RUNTIME_DIR/sway.conf"
# Floating keeps the app's own window size, like on a desktop (tests assume a 1440-wide window).
printf '%s\n' 'output HEADLESS-1 resolution 1920x1080' 'default_border none' 'for_window [all] floating enable' >"$cfg"
WLR_BACKENDS=headless WLR_LIBINPUT_NO_DEVICES=1 WLR_RENDERER=pixman sway -c "$cfg" >"$XDG_RUNTIME_DIR/sway.log" 2>&1 &
SP=$!
for i in $(seq 100); do s=$(ls "$XDG_RUNTIME_DIR" | grep -m1 '^wayland-[0-9]*$') && break; sleep 0.1; done
[ -n "$s" ] || { echo "wl-run: sway did not start" >&2; cat "$XDG_RUNTIME_DIR/sway.log" >&2; kill $SP; exit 1; }
export WAYLAND_DISPLAY=$s
"$@"; rc=$?
kill $SP 2>/dev/null; wait $SP 2>/dev/null; rm -rf "$XDG_RUNTIME_DIR"
exit $rc
