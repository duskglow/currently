#!/bin/bash
# Tests Currently's NUT protocol server against the real NUT clients (upsc,
# upscmd, upsrw and upsmon as primary and secondary through a simulated
# power failure).
#
#   test/integration/nut-clients.sh path/to/currently
#
# Uses the installed NUT clients (`apt install nut-client`), or on Debian
# and Ubuntu downloads and unpacks them without installing when they are
# missing. Linux only. Nothing outside /tmp/currently-nut-test is touched; the
# upsmon shutdown commands only write marker files.
set -u
CURRENTLY="$(realpath "${1:?usage: $0 path/to/currently}")"
T=/tmp/currently-nut-test
rm -rf "$T"; mkdir -p "$T"/{pkgs,root,state1,state2}

# nutbin NAME [ROOT] prints the path of a NUT program (an ELF binary, not
# the wrapper scripts some distributions put in /sbin), or nothing.
nutbin() {
  for d in lib/nut usr/lib/nut usr/libexec/nut usr/sbin sbin usr/bin bin; do
    f="${2:-}/$d/$1"
    if [ -x "$f" ] && [ "$(head -c4 "$f" 2>/dev/null | tail -c3)" = ELF ]; then echo "$f"; return; fi
  done
}

if command -v upsc >/dev/null && [ -n "$(nutbin upsmon)" ]; then
  B="$(dirname "$(command -v upsc)")"
  UPSMON="$(nutbin upsmon)"
else
  echo "NUT clients not installed; downloading them into $T"
  # libnss3 is pinned to the release version, which stays on the mirrors
  # even when the local package index is stale.
  (cd "$T/pkgs" && apt-get download nut-client libupsclient6t64 libnss3=2:3.98-1build1 libnspr4 >/dev/null 2>&1) ||
    { echo "apt-get download failed; install nut-client instead"; exit 1; }
  for d in "$T"/pkgs/*.deb; do dpkg -x "$d" "$T/root"; done
  LIB="$(find "$T/root" -name 'libupsclient.so.6*' | head -1)"
  export LD_LIBRARY_PATH="$(dirname "$LIB"):$T/root/usr/lib/x86_64-linux-gnu:$T/root/lib/x86_64-linux-gnu"
  B="$T/root/bin"; [ -x "$B/upsc" ] || B="$T/root/usr/bin"
  UPSMON="$(nutbin upsmon "$T/root")"
fi
echo "NUT client: $("$B/upsc" -V 2>&1 | head -1)"

cat > "$T/currently.yaml" <<'EOF'
ups:
  sim:
    driver: simulator
    description: WSL test UPS
    interval: 1s
    speed: 60
    runtime: 10m
listen:
  http: 127.0.0.1:19300
  nut: 127.0.0.1:13493
  control: /tmp/currently-nut-test/currently.sock
users:
  upsmon: {password: pw1, role: primary}
  mon: {password: pw2, role: secondary}
  admin: {password: pw3, role: admin}
EOF
"$CURRENTLY" --config "$T/currently.yaml" serve --log-level debug >"$T/currently.log" 2>&1 &
CURRENTLY_PID=$!
sleep 2
U=sim@localhost:13493
pass=0; fail=0
ok()  { pass=$((pass+1)); echo "PASS $1"; }
bad() { fail=$((fail+1)); echo "FAIL $1"; }
show() { echo "$1" | sed 's/^/     /' | head -"${2:-5}"; }
# expect NAME PATTERN COMMAND...: the output must match PATTERN.
expect() {
  local name="$1" pat="$2"; shift 2
  local out; out=$("$@" 2>&1)
  if echo "$out" | grep -qE -- "$pat"; then ok "$name"; else bad "$name (wanted /$pat/)"; fi
  show "$out" "${SHOW:-4}"
}
# succeeds NAME COMMAND...: the command must exit 0.
succeeds() {
  local name="$1"; shift
  local out; out=$("$@" 2>&1); local rc=$?
  if [ $rc -eq 0 ]; then ok "$name"; else bad "$name (exit $rc)"; fi
  [ -n "$out" ] && show "$out" 3
}

echo "--- upsc"
expect "upsc -l lists the UPS" '^sim$' "$B/upsc" -l localhost:13493
expect "upsc -L shows the description" 'sim: WSL test UPS' "$B/upsc" -L localhost:13493
expect "upsc lists variables" '^ups.status: OL' "$B/upsc" $U
expect "upsc reads one variable" '^100$' "$B/upsc" $U battery.charge
expect "upsc reports a missing variable" 'not supported' "$B/upsc" $U no.such.var
expect "upsc reports an unknown UPS" 'Unknown UPS' "$B/upsc" nope@localhost:13493

echo "--- upscmd / upsrw"
expect "upscmd -l lists commands" 'sim.power.fail' "$B/upscmd" -l $U
expect "upscmd refuses a secondary user" 'ACCESS-DENIED' "$B/upscmd" -u mon -p pw2 $U beeper.disable
succeeds "upscmd runs a command as admin" "$B/upscmd" -u admin -p pw3 $U beeper.disable
sleep 1.5
expect "the command took effect" '^disabled$' "$B/upsc" $U ups.beeper.status
SHOW=12 expect "upsrw lists writable variables" 'ups.load' "$B/upsrw" $U
succeeds "upsrw sets a variable" "$B/upsrw" -s ups.load=50 -u admin -p pw3 $U
sleep 1.5
expect "the variable changed" '^50$' "$B/upsc" $U ups.load

echo "--- upsmon (primary + secondary) through a power failure"
start_upsmon() { # index, "user password role"
  local i=$1 who=$2
  mkdir -p "$T/etc$i"
  cat > "$T/etc$i/upsmon.conf" <<EOF
MONITOR $U 1 $who
MINSUPPLIES 1
SHUTDOWNCMD "/bin/sh -c 'echo shutdown >> $T/state$i/shutdown.flag'"
POLLFREQ 1
POLLFREQALERT 1
HOSTSYNC 10
DEADTIME 15
FINALDELAY 0
POWERDOWNFLAG $T/state$i/killpower
EOF
  chmod 600 "$T/etc$i/upsmon.conf"
  NUT_CONFPATH="$T/etc$i" NUT_STATEPATH="$T/state$i" NUT_ALTPIDPATH="$T/state$i" \
    "$UPSMON" -D -F -u "$(id -un)" >"$T/upsmon$i.log" 2>&1 &
}
start_upsmon 1 "upsmon pw1 primary"; PID1=$!
start_upsmon 2 "mon pw2 secondary"; PID2=$!
sleep 4
expect "upsc -c shows both upsmon clients" '127.0.0.1' "$B/upsc" -c $U
"$CURRENTLY" --config "$T/currently.yaml" cmd sim sim.power.fail >/dev/null
echo "     (mains failed; waiting for low battery, FSD and both shutdown commands)"
for s in $(seq 1 90); do
  [ -f "$T/state1/shutdown.flag" ] && [ -f "$T/state2/shutdown.flag" ] && break
  sleep 1
done
echo "     done after ${s}s"
expect "currently recorded FSD from the primary" 'forced shutdown \(FSD\) set' cat "$T/currently.log"
succeeds "secondary upsmon ran its shutdown command" test -f "$T/state2/shutdown.flag"
succeeds "primary upsmon ran its shutdown command" test -f "$T/state1/shutdown.flag"
succeeds "primary wrote its POWERDOWNFLAG" test -f "$T/state1/killpower"
echo "     primary upsmon log (excerpt):"
grep -iE "battery|FSD|forced|shutdown|login|primary|secondar" "$T/upsmon1.log" | head -14 | sed 's/^/       /'
echo "     secondary upsmon log (excerpt):"
grep -iE "battery|FSD|forced|shutdown|login" "$T/upsmon2.log" | head -8 | sed 's/^/       /'

kill $PID1 $PID2 $CURRENTLY_PID 2>/dev/null
wait 2>/dev/null
echo "--- result: $pass passed, $fail failed"
[ $fail -eq 0 ]
