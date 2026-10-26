#!/bin/bash
# Tests nutng's SNMP agent against the net-snmp tools: snmpget, snmpwalk
# and snmpbulkwalk with v1, v2c and v3 (SHA-256/AES and MD5), and
# snmptrapd receiving the UPS-MIB traps through a simulated outage.
#
#   test/integration/snmp.sh path/to/nutng
#
# Needs the snmp and snmptrapd packages (`apt install snmp snmptrapd`).
# Linux only. Nothing outside /tmp/nutng-snmp-test is touched.
set -u
NUTNG="$(realpath "${1:?usage: $0 path/to/nutng}")"
T=/tmp/nutng-snmp-test
rm -rf "$T"; mkdir -p "$T"
for c in snmpget snmpwalk snmpbulkwalk snmpset snmptrapd; do
  command -v $c >/dev/null || { echo "$c not found; install snmp and snmptrapd"; exit 1; }
done
echo "net-snmp: $(snmpget -V 2>&1)"

cat > "$T/nutng.yaml" <<'EOF'
ups:
  rack:
    driver: simulator
    description: Rack UPS
  lab:
    driver: simulator
    scenario: cycle
    online_for: 4s
    outage_for: 8s
listen:
  http: off
  metrics: off
  nut: off
  control: /tmp/nutng-snmp-test/nutng.sock
  snmp:
    addr: 127.0.0.1:11161
    community: s3cret
    location: Test lab
    users:
      mon:
        auth: sha256
        auth_password: authpassword
        privacy: aes
        privacy_password: privpassword
      md5user:
        auth: md5
        auth_password: authpassword
    traps:
      - target: 127.0.0.1:11162
        community: traps
EOF
echo "authCommunity log traps" > "$T/snmptrapd.conf"
snmptrapd -C -c "$T/snmptrapd.conf" -f -Lf "$T/traps.log" -n udp:127.0.0.1:11162 &
TRAPD=$!
"$NUTNG" --config "$T/nutng.yaml" serve --log-level debug >"$T/nutng.log" 2>&1 &
NUTNG_PID=$!
sleep 2

A=127.0.0.1:11161
V2="-v2c -c s3cret -On"
V3="-v3 -l authPriv -u mon -a SHA-256 -A authpassword -x AES -X privpassword -On"
UPS=.1.3.6.1.2.1.33
pass=0; fail=0
ok()  { pass=$((pass+1)); echo "PASS $1"; }
bad() { fail=$((fail+1)); echo "FAIL $1"; }
show() { echo "$1" | sed 's/^/     /' | head -"${2:-4}"; }
# expect NAME PATTERN COMMAND...: the output must match PATTERN.
expect() {
  local name="$1" pat="$2"; shift 2
  local out; out=$("$@" 2>&1)
  if echo "$out" | grep -qE -- "$pat"; then ok "$name"; else bad "$name (wanted /$pat/)"; fi
  show "$out" "${SHOW:-3}"
}
# count NAME MIN COMMAND...: the command must print at least MIN lines.
count() {
  local name="$1" min="$2"; shift 2
  local n; n=$("$@" 2>/dev/null | wc -l)
  if [ "$n" -ge "$min" ]; then ok "$name ($n objects)"; else bad "$name ($n objects, wanted $min)"; fi
}

echo "--- v2c"
expect "sysDescr names nut-ng" 'STRING: "nut-ng' snmpget $V2 $A .1.3.6.1.2.1.1.1.0
expect "sysObjectID is the UPS-MIB" "OID: $UPS\$" snmpget $V2 $A .1.3.6.1.2.1.1.2.0
expect "sysLocation from the config" 'Test lab' snmpget $V2 $A .1.3.6.1.2.1.1.6.0
expect "upsIdentName is the default UPS" 'STRING: "rack"' snmpget $V2 $A $UPS.1.1.5.0
expect "upsBatteryStatus is normal" 'INTEGER: 2$' snmpget $V2 $A $UPS.1.2.1.0
expect "upsEstimatedChargeRemaining" 'INTEGER: [0-9]+$' snmpget $V2 $A $UPS.1.2.4.0
expect "upsOutputSource is normal" 'INTEGER: 3$' snmpget $V2 $A $UPS.1.4.1.0
expect "upsInputVoltage" "$UPS.1.3.3.1.3.1 = INTEGER: [0-9]+" snmpget $V2 $A $UPS.1.3.3.1.3.1
expect "a missing object is noSuchObject" 'No Such Object' snmpget $V2 $A $UPS.1.2.6.0
count "snmpwalk of the UPS-MIB" 25 snmpwalk $V2 $A $UPS
count "snmpbulkwalk of everything" 40 snmpbulkwalk $V2 $A .1
W=$(snmpwalk $V2 $A $UPS 2>&1); BW=$(snmpbulkwalk $V2 $A $UPS 2>&1)
if [ "$W" = "$BW" ]; then ok "walk and bulk walk agree"; else bad "walk and bulk walk differ"; fi
expect "community@ups selects a UPS" 'STRING: "lab"' snmpget -v2c -c s3cret@lab -On $A $UPS.1.1.5.0
expect "writes are refused" 'notWritable' snmpset $V2 $A .1.3.6.1.2.1.1.4.0 s x
expect "a wrong community gets no answer" 'Timeout' snmpget -v2c -c public -t 0.5 -r 0 $A .1.3.6.1.2.1.1.1.0

echo "--- v1"
expect "v1 get" 'INTEGER: [0-9]+$' snmpget -v1 -c s3cret -On $A $UPS.1.2.4.0
expect "v1 missing object is noSuchName" 'noSuchName' snmpget -v1 -c s3cret -On $A $UPS.1.2.6.0
count "v1 walk" 25 snmpwalk -v1 -c s3cret -On $A $UPS

echo "--- v3"
expect "authPriv SHA-256/AES get" 'INTEGER: [0-9]+$' snmpget $V3 $A $UPS.1.2.4.0
count "authPriv bulk walk" 25 snmpbulkwalk $V3 $A $UPS
expect "the context name selects a UPS" 'STRING: "lab"' snmpget $V3 -n lab $A $UPS.1.1.5.0
expect "an unknown context is refused" 'Bad context|nknown context' snmpget $V3 -n nope -t 1 -r 0 $A $UPS.1.1.5.0
count "authNoPriv MD5 walk" 25 snmpwalk -v3 -l authNoPriv -u md5user -a MD5 -A authpassword -On $A $UPS
expect "a wrong password is refused" 'Authentication failure' snmpget -v3 -l authPriv -u mon -a SHA-256 -A wrongpassword -x AES -X privpassword $A $UPS.1.2.4.0
expect "an unknown user is refused" 'Unknown user' snmpget -v3 -l authNoPriv -u nobody -a SHA -A authpassword $A $UPS.1.2.4.0
expect "privacy for a user without it is refused" 'Unsupported security level' snmpget -v3 -l authPriv -u md5user -a MD5 -A authpassword -x AES -X privpassword $A $UPS.1.2.4.0
expect "a user with privacy must use it" 'authorizationError' snmpget -v3 -l authNoPriv -u mon -a SHA-256 -A authpassword $A $UPS.1.2.4.0

echo "--- power failure on lab (traps and alarms)"
for s in $(seq 1 20); do
  grep -q '\.1\.3\.6\.1\.2\.1\.33\.2\.4' "$T/traps.log" 2>/dev/null && break
  sleep 1
done
expect "upsTrapOnBattery received" 'OID: (iso|\.1)\.3\.6\.1\.2\.1\.33\.2\.1\b' cat "$T/traps.log"
expect "upsTrapAlarmEntryAdded (onBattery) received" '33\.2\.3.*33\.1\.6\.3\.2' cat "$T/traps.log"
expect "upsTrapAlarmEntryRemoved received" '33\.2\.4.*33\.1\.6\.3\.2' cat "$T/traps.log"
expect "traps name the UPS" 'STRING: "lab"' cat "$T/traps.log"
for s in $(seq 1 15); do
  snmpget -v2c -c s3cret@lab -On $A $UPS.1.4.1.0 2>/dev/null | grep -q 'INTEGER: 5$' && break
  sleep 1
done
expect "during the outage upsOutputSource is battery" 'INTEGER: 5$' snmpget -v2c -c s3cret@lab -On $A $UPS.1.4.1.0
expect "and the alarm table has onBattery" "OID: $UPS.1.6.3.2" snmpwalk -v2c -c s3cret@lab -On $A $UPS.1.6.2
expect "and upsSecondsOnBattery counts" 'INTEGER: [0-9]+$' snmpget -v2c -c s3cret@lab -On $A $UPS.1.2.2.0

echo "--- doctor"
expect "nutng doctor reaches the agent" 'SNMP agent answers' "$NUTNG" --config "$T/nutng.yaml" doctor

kill $NUTNG_PID $TRAPD 2>/dev/null
wait 2>/dev/null
echo "--- result: $pass passed, $fail failed"
[ $fail -eq 0 ]
