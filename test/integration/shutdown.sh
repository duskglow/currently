#!/bin/bash
# Rehearses a power failure across machines, without shutting anything
# down: every shutdown command only writes a timestamped marker file, and
# CURRENTLY_NEVER_SHUTDOWN=1 blocks Currently's built-in OS shutdown regardless.
#
#   test/integration/shutdown.sh path/to/currently
#
# 1. A Currently primary with a simulated UPS, followed by
#    - a Currently secondary (nut driver), and
#    - NUT's own upsmon as a secondary.
#    After a simulated outage, both secondaries must shut down and log out
#    before the primary runs its shutdown command.
# 2. A Currently that follows NUT's own upsd (with the dummy-ups driver) must
#    shut down when that UPS reports OB LB.
#
# Uses the installed NUT (apt install nut-client nut-server), or downloads
# and unpacks the packages into /tmp without installing. Linux only.
set -u
CURRENTLY="$(realpath "${1:?usage: $0 path/to/currently}")"
export CURRENTLY_NEVER_SHUTDOWN=1
T=/tmp/currently-shutdown-test
rm -rf "$T"; mkdir -p "$T"/{pkgs,root,upsmon,upsd,markers}

# nutbin NAME [ROOT] prints the path of a NUT program (an ELF binary, not
# the wrapper scripts some distributions put in /sbin), or nothing.
nutbin() {
  for d in lib/nut usr/lib/nut usr/libexec/nut usr/sbin sbin usr/bin bin; do
    f="${2:-}/$d/$1"
    if [ -x "$f" ] && [ "$(head -c4 "$f" 2>/dev/null | tail -c3)" = ELF ]; then echo "$f"; return; fi
  done
}

if command -v upsc >/dev/null && [ -n "$(nutbin upsmon)" ] && [ -n "$(nutbin upsd)" ] && [ -n "$(nutbin dummy-ups)" ]; then
  UPSMON="$(nutbin upsmon)" UPSD="$(nutbin upsd)" DUMMY="$(nutbin dummy-ups)"
else
  echo "NUT not installed; downloading it into $T"
  (cd "$T/pkgs" && apt-get download nut-client nut-server libupsclient6t64 libnutscan2t64 libnss3=2:3.98-1build1 libnspr4 >/dev/null 2>&1) ||
    { echo "apt-get download failed; install nut-client and nut-server instead"; exit 1; }
  for d in "$T"/pkgs/*.deb; do dpkg -x "$d" "$T/root"; done
  LIB="$(find "$T/root" -name 'libupsclient.so.6*' | head -1)"
  export LD_LIBRARY_PATH="$(dirname "$LIB"):$T/root/usr/lib/x86_64-linux-gnu:$T/root/lib/x86_64-linux-gnu"
  UPSMON="$(nutbin upsmon "$T/root")" UPSD="$(nutbin upsd "$T/root")" DUMMY="$(nutbin dummy-ups "$T/root")"
fi
ME="$(id -un)"
pass=0; fail=0
ok()  { pass=$((pass+1)); echo "PASS $1"; }
bad() { fail=$((fail+1)); echo "FAIL $1"; }
marker() { echo "/bin/sh -c 'date +%s.%N > $T/markers/$1'"; }
PIDS=()
cleanup() { kill "${PIDS[@]}" 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT

echo "--- part 1: currently primary, currently secondary, NUT upsmon secondary"
cat > "$T/primary.yaml" <<EOF
ups:
  sim:
    driver: simulator
    interval: 500ms
    speed: 120
    runtime: 10m
listen:
  http: 127.0.0.1:19500
  nut: 127.0.0.1:13500
  control: $T/primary.sock
users:
  mon: {password: pw, role: secondary}
shutdown:
  wait_for_secondaries: 30s
  final_delay: 1s
  hooks: ["$(marker primary-hook)"]
  command: "$(marker primary)"
EOF
cat > "$T/secondary.yaml" <<EOF
ups:
  office:
    driver: nut
    host: 127.0.0.1:13500
    ups: sim
    username: mon
    password: pw
    interval: 1s
listen:
  http: off
  metrics: off
  nut: off
  control: $T/secondary.sock
shutdown:
  final_delay: 1s
  command: "$(marker currently-secondary)"
EOF
cat > "$T/upsmon/upsmon.conf" <<EOF
MONITOR sim@127.0.0.1:13500 1 mon pw secondary
MINSUPPLIES 1
SHUTDOWNCMD "$(marker upsmon-secondary)"
POLLFREQ 1
POLLFREQALERT 1
DEADTIME 15
FINALDELAY 0
EOF
chmod 600 "$T/upsmon/upsmon.conf"

"$CURRENTLY" --config "$T/primary.yaml" serve >"$T/primary.log" 2>&1 & PIDS+=($!)
sleep 2
"$CURRENTLY" --config "$T/secondary.yaml" serve >"$T/secondary.log" 2>&1 & PIDS+=($!)
NUT_CONFPATH="$T/upsmon" NUT_STATEPATH="$T/upsmon" NUT_ALTPIDPATH="$T/upsmon" \
  "$UPSMON" -F -u "$ME" >"$T/upsmon.log" 2>&1 & PIDS+=($!)
sleep 4

P="$CURRENTLY --config $T/primary.yaml"
if $P test shutdown | grep -q "logged in now: 127.0.0.1, 127.0.0.1"; then ok "both secondaries are logged in to the primary"
else bad "both secondaries are logged in to the primary"; $P test shutdown | sed 's/^/     /'; fi

$P cmd sim sim.power.fail >/dev/null
echo "     (mains failed; waiting for low battery)"
for s in $(seq 1 90); do [ -f "$T/markers/primary" ] && break; sleep 1; done

for m in upsmon-secondary currently-secondary primary-hook primary; do
  if [ -f "$T/markers/$m" ]; then ok "$m shutdown command ran"; else bad "$m shutdown command ran"; fi
done
if [ -f "$T/markers/primary" ] && [ -f "$T/markers/currently-secondary" ] && [ -f "$T/markers/upsmon-secondary" ]; then
  p=$(cat "$T/markers/primary"); a=$(cat "$T/markers/currently-secondary"); b=$(cat "$T/markers/upsmon-secondary")
  if awk "BEGIN{exit !($p > $a && $p > $b)}"; then ok "the primary shut down after both secondaries"
  else bad "the primary shut down after both secondaries (primary $p, currently $a, upsmon $b)"; fi
fi
STEPS="$($P test shutdown | sed -n '/^Last shutdown/,/^$/p')"
echo "$STEPS" | sed 's/^/     /'
if echo "$STEPS" | grep -q "Wait up to 30s for NUT clients to log out → done"; then ok "the primary saw both secondaries log out"
else bad "the primary saw both secondaries log out"; fi
cleanup; PIDS=()

echo "--- part 2: currently following NUT's own upsd (dummy-ups)"
U="$T/upsd"
cat > "$U/ups.conf" <<EOF
[dummy]
  driver = dummy-ups
  port = $U/dummy.dev
  mode = dummy-once
  desc = "NUT dummy UPS"
EOF
printf 'ups.status: OL\nbattery.charge: 100\nbattery.runtime: 1800\n' > "$U/dummy.dev"
cat > "$U/upsd.conf" <<EOF
LISTEN 127.0.0.1 13501
EOF
cat > "$U/upsd.users" <<EOF
[mon]
  password = pw
  upsmon secondary
EOF
chmod 600 "$U"/upsd.users
export NUT_CONFPATH="$U" NUT_STATEPATH="$U" NUT_ALTPIDPATH="$U"
"$DUMMY" -a dummy -u "$ME" -F >"$T/dummy.log" 2>&1 & PIDS+=($!)
sleep 2
"$UPSD" -u "$ME" -F >"$T/upsd.log" 2>&1 & PIDS+=($!)
sleep 2
unset NUT_CONFPATH NUT_STATEPATH NUT_ALTPIDPATH
cat > "$T/follower.yaml" <<EOF
ups:
  nas:
    driver: nut
    host: 127.0.0.1:13501
    ups: dummy
    username: mon
    password: pw
    interval: 1s
listen:
  http: off
  metrics: off
  nut: off
  control: $T/follower.sock
shutdown:
  final_delay: 1s
  command: "$(marker upsd-follower)"
EOF
"$CURRENTLY" --config "$T/follower.yaml" serve >"$T/follower.log" 2>&1 & PIDS+=($!)
sleep 3
F="$CURRENTLY --config $T/follower.yaml"
if $F status | grep -q "online"; then ok "currently reads the UPS from NUT's upsd"
else bad "currently reads the UPS from NUT's upsd"; $F status 2>&1 | sed 's/^/     /'; tail -5 "$T/upsd.log" "$T/dummy.log" | sed 's/^/     /'; fi
sleep 1
printf 'ups.status: OB LB\nbattery.charge: 8\nbattery.runtime: 60\n' > "$U/dummy.dev"
for s in $(seq 1 30); do [ -f "$T/markers/upsd-follower" ] && break; sleep 1; done
if [ -f "$T/markers/upsd-follower" ]; then ok "currently shut down when upsd's UPS went OB LB"
else bad "currently shut down when upsd's UPS went OB LB"; $F test shutdown | head -12 | sed 's/^/     /'; fi

echo "--- result: $pass passed, $fail failed"
[ $fail -eq 0 ]
