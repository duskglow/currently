// Package snmp is a read-only SNMP agent (v1, v2c and v3 with USM) that
// serves the UPS-MIB (RFC 1628), so LibreNMS, PRTG, Zabbix, Observium and
// other network monitors can poll nutng like a UPS network card.
//
// Each UPS is a separate UPS-MIB view: SNMPv3 selects it with the context
// name, and v1/v2c with a community of the form "community@ups". Without
// either, the default UPS is served.
package snmp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Options configure the agent.
type Options struct {
	// Community is the v1/v2c read-only community. Empty disables v1 and
	// v2c.
	Community string
	// DefaultUPS is served when the request does not name a UPS; empty
	// selects the first one.
	DefaultUPS string
	// Users are SNMPv3 users.
	Users []User
	// EngineID is the SNMPv3 engine ID; empty derives one from the host
	// name.
	EngineID []byte
	// Traps are SNMPv2c trap receivers.
	Traps    []TrapTarget
	Location string
	Contact  string
	Version  string
	Log      *slog.Logger
	// Now replaces time.Now (tests).
	Now func() time.Time
}

// User is an SNMPv3 user. Auth is "none", "md5", "sha", "sha224",
// "sha256", "sha384" or "sha512"; Privacy is "none" or "aes".
type User struct {
	Name            string
	Auth            string
	AuthPassword    string
	Privacy         string
	PrivacyPassword string
}

// TrapTarget is a receiver of SNMPv2c traps, as host:port.
type TrapTarget struct {
	Addr      string
	Community string
}

// Error statuses.
const (
	errNoError     = 0
	errTooBig      = 1
	errNoSuchName  = 2
	errGenErr      = 5
	errAuthorize   = 16
	errNotWritable = 17
)

const (
	maxMessage   = 65507 // largest UDP payload
	bulkTarget   = 1472  // GetBulk responses are trimmed to one Ethernet frame
	maxBulkVars  = 512
	trapInterval = time.Minute // upsTrapOnBattery repeats while on battery
)

// Agent is an SNMP agent.
type Agent struct {
	host     sdk.Host
	opts     Options
	log      *slog.Logger
	now      func() time.Time
	start    time.Time
	boots    int64
	engineID []byte
	users    map[string]*usmUser
	salt     atomic.Uint64
	reqID    atomic.Int32

	mu         sync.Mutex
	track      map[string]*upsTrack
	nextAlarm  uint32
	stats      usmStats
	unknownCtx int64 // snmpUnknownContexts
	conn       net.PacketConn
}

// upsTrack follows one UPS between requests: how long it has been on
// battery, how often mains failed, and its alarm table.
type upsTrack struct {
	onBattery     bool
	obSince       time.Time
	lineBads      int64
	alarms        []alarmEntry
	lastOBTrap    time.Time
	everConnected bool
}

type alarmEntry struct {
	id, descr uint32
	since     int64 // sysUpTime when raised
}

func (t *upsTrack) secondsOnBattery(now time.Time) int64 {
	if !t.onBattery {
		return 0
	}
	return int64(now.Sub(t.obSince) / time.Second)
}

// New returns an agent. It derives the SNMPv3 keys, which takes a moment
// per user.
func New(host sdk.Host, opts Options) (*Agent, error) {
	a := &Agent{host: host, opts: opts, log: opts.Log, now: opts.Now, track: map[string]*upsTrack{}, nextAlarm: 1}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	a.start = a.now()
	// engineBoots must grow at every restart; the clock does that without
	// a state file.
	a.boots = max(1, a.start.Unix()-1_700_000_000)
	a.engineID = opts.EngineID
	if len(a.engineID) == 0 {
		a.engineID = DefaultEngineID()
	}
	a.salt.Store(uint64(a.start.UnixNano()))
	a.users = map[string]*usmUser{}
	for _, u := range opts.Users {
		uu, err := newUSMUser(u, a.engineID)
		if err != nil {
			return nil, err
		}
		a.users[u.Name] = uu
	}
	return a, nil
}

// DefaultEngineID derives a stable engine ID from the host name (RFC 3411
// format 5, enterprise 0).
func DefaultEngineID() []byte {
	h, _ := os.Hostname()
	sum := sha256.Sum256([]byte("nutng snmp engine " + h))
	return append([]byte{0x80, 0, 0, 0, 5}, sum[:8]...)
}

// EngineID returns the SNMPv3 engine ID.
func (a *Agent) EngineID() []byte { return a.engineID }

// Serve answers requests on pc until ctx is cancelled.
func (a *Agent) Serve(ctx context.Context, pc net.PacketConn) error {
	a.mu.Lock()
	a.conn = pc
	a.mu.Unlock()
	go func() {
		<-ctx.Done()
		pc.Close()
	}()
	go a.watch(ctx)

	buf := make([]byte, maxMessage)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			a.log.Debug("snmp read failed", "error", err)
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		if resp := a.Handle(pkt); resp != nil {
			if _, err := pc.WriteTo(resp, from); err != nil {
				a.log.Debug("snmp write failed", "to", from.String(), "error", err)
			}
		}
	}
}

// Handle answers one request message, returning nil when there is
// nothing to send back.
func (a *Agent) Handle(pkt []byte) []byte {
	resp, err := a.handle(pkt)
	if err != nil {
		a.log.Debug("snmp request dropped", "error", err)
		return nil
	}
	return resp
}

func (a *Agent) handle(pkt []byte) ([]byte, error) {
	top, err := newDecoder(pkt).sub(tagSequence)
	if err != nil {
		return nil, err
	}
	version, err := top.int()
	if err != nil {
		return nil, err
	}
	switch version {
	case 0, 1:
		return a.handleCommunity(int(version), top)
	case 3:
		return a.handleV3(pkt)
	}
	return nil, fmt.Errorf("unsupported SNMP version %d", version)
}

func (a *Agent) handleCommunity(version int, top *decoder) ([]byte, error) {
	community, _, err := top.octets()
	if err != nil {
		return nil, err
	}
	pdu, err := top.pdu()
	if err != nil {
		return nil, err
	}
	ups, ok := a.communityUPS(string(community))
	if !ok {
		return nil, errors.New("wrong community")
	}
	if version == 0 && pdu.Type == pduGetBulk {
		return nil, errors.New("GetBulk in an SNMPv1 message")
	}
	resp, ok := a.process(pdu, version == 0, ups, maxMessage-64-len(community))
	if !ok {
		return nil, nil
	}
	var c []byte
	c = appendInt(c, int64(version))
	c = appendOctets(c, community)
	c = append(c, resp.encode()...)
	return appendTLV(nil, tagSequence, c), nil
}

// communityUPS checks a v1/v2c community, returning the UPS it selects.
func (a *Agent) communityUPS(community string) (string, bool) {
	want := a.opts.Community
	if want == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(community), []byte(want)) == 1 {
		return a.defaultUPS(), true
	}
	base, ups, found := strings.Cut(community, "@")
	if !found || subtle.ConstantTimeCompare([]byte(base), []byte(want)) != 1 {
		return "", false
	}
	if _, ok := a.host.GetUPS(ups); !ok {
		return "", false
	}
	return ups, true
}

func (a *Agent) defaultUPS() string {
	if a.opts.DefaultUPS != "" {
		return a.opts.DefaultUPS
	}
	if list := a.host.ListUPS(); len(list) > 0 {
		return list[0].Name
	}
	return ""
}

// uptime returns sysUpTime in hundredths of a second.
func (a *Agent) uptime(now time.Time) int64 {
	return int64(now.Sub(a.start)/(10*time.Millisecond)) & 0xffffffff
}

// tree returns every object for a UPS view, sorted.
func (a *Agent) tree(ups string) []VarBind {
	now := a.now()
	var vbs []VarBind
	host, _ := os.Hostname()
	sys := func(arc uint32, v Value) { vbs = append(vbs, VarBind{oidSystem.Append(arc, 0), v}) }
	sys(1, stringValue(fmt.Sprintf("nut-ng %s UPS monitor (%s/%s)", a.opts.Version, runtime.GOOS, runtime.GOARCH)))
	sys(2, oidValue(oidUPSMIB))
	sys(3, ticksValue(a.uptime(now)))
	sys(4, stringValue(a.opts.Contact))
	sys(5, stringValue(host))
	sys(6, stringValue(a.opts.Location))
	sys(7, intValue(72)) // application and end-to-end services

	if u, ok := a.host.GetUPS(ups); ok {
		a.mu.Lock()
		t, _ := a.refreshLocked(u, now)
		snapshot := *t
		snapshot.alarms = slices.Clone(t.alarms)
		a.mu.Unlock()
		vbs = append(vbs, upsTree(u, &snapshot, a.opts.Version, now)...)
	}

	vbs = append(vbs,
		VarBind{oidSnmpEngine.Append(1, 0), bytesValue(a.engineID)},
		VarBind{oidSnmpEngine.Append(2, 0), intValue(a.boots)},
		VarBind{oidSnmpEngine.Append(3, 0), intValue(a.engineTime(now))},
		VarBind{oidSnmpEngine.Append(4, 0), intValue(maxMessage)},
	)
	a.mu.Lock()
	for i, n := range a.stats {
		vbs = append(vbs, VarBind{oidUSMStats.Append(uint32(i+1), 0), counterValue(n)})
	}
	vbs = append(vbs, VarBind{oidUnknownContexts, counterValue(a.unknownCtx)})
	a.mu.Unlock()
	slices.SortFunc(vbs, func(x, y VarBind) int { return x.Name.Compare(y.Name) })
	return vbs
}

// process answers a request PDU. ok is false when the PDU type gets no
// response.
func (a *Agent) process(req PDU, v1 bool, ups string, limit int) (PDU, bool) {
	resp := PDU{Type: pduResponse, RequestID: req.RequestID}
	switch req.Type {
	case pduGet, pduGetNext, pduGetBulk, pduSet:
	default:
		return resp, false
	}
	if req.Type == pduSet {
		resp.VarBinds = req.VarBinds
		resp.ErrStatus, resp.ErrIndex = errNotWritable, 1
		if v1 {
			resp.ErrStatus = errNoSuchName
		}
		return resp, true
	}

	tree := a.tree(ups)
	fail := func(status, index int) (PDU, bool) {
		resp.ErrStatus, resp.ErrIndex = status, index
		resp.VarBinds = req.VarBinds
		return resp, true
	}

	switch req.Type {
	case pduGet:
		for i, vb := range req.VarBinds {
			v, ok := lookup(tree, vb.Name)
			if !ok {
				if v1 {
					return fail(errNoSuchName, i+1)
				}
				v = missing(tree, vb.Name)
			}
			resp.VarBinds = append(resp.VarBinds, VarBind{vb.Name, v})
		}
	case pduGetNext:
		for i, vb := range req.VarBinds {
			next, ok := after(tree, vb.Name)
			if !ok {
				if v1 {
					return fail(errNoSuchName, i+1)
				}
				next = VarBind{vb.Name, Value{Tag: tagEndOfMibView}}
			}
			resp.VarBinds = append(resp.VarBinds, next)
		}
	case pduGetBulk:
		nonRep := min(max(req.ErrStatus, 0), len(req.VarBinds))
		maxRep := max(req.ErrIndex, 0)
		for _, vb := range req.VarBinds[:nonRep] {
			next, ok := after(tree, vb.Name)
			if !ok {
				next = VarBind{vb.Name, Value{Tag: tagEndOfMibView}}
			}
			resp.VarBinds = append(resp.VarBinds, next)
		}
		cursors := make([]OID, 0, len(req.VarBinds)-nonRep)
		for _, vb := range req.VarBinds[nonRep:] {
			cursors = append(cursors, vb.Name)
		}
		for r := 0; r < maxRep && len(cursors) > 0 && len(resp.VarBinds) < maxBulkVars; r++ {
			done := true
			for i, c := range cursors {
				next, ok := after(tree, c)
				if !ok {
					next = VarBind{c, Value{Tag: tagEndOfMibView}}
				} else {
					done = false
					cursors[i] = next.Name
				}
				resp.VarBinds = append(resp.VarBinds, next)
			}
			if done {
				break
			}
		}
		// Trim to fit; a GetBulk response may carry fewer variables.
		for len(resp.VarBinds) > 1 && len(resp.encode()) > min(limit, bulkTarget) {
			resp.VarBinds = resp.VarBinds[:len(resp.VarBinds)*3/4]
		}
	}
	if len(resp.encode()) > limit {
		resp.VarBinds = nil
		resp.ErrStatus, resp.ErrIndex = errTooBig, 0
		if v1 {
			resp.VarBinds = req.VarBinds
		}
	}
	return resp, true
}

func lookup(tree []VarBind, name OID) (Value, bool) {
	i, ok := slices.BinarySearchFunc(tree, name, func(vb VarBind, o OID) int { return vb.Name.Compare(o) })
	if !ok {
		return Value{}, false
	}
	return tree[i].Value, true
}

func after(tree []VarBind, name OID) (VarBind, bool) {
	i, ok := slices.BinarySearchFunc(tree, name, func(vb VarBind, o OID) int { return vb.Name.Compare(o) })
	if ok {
		i++
	}
	if i >= len(tree) {
		return VarBind{}, false
	}
	return tree[i], true
}

// missing returns noSuchInstance when name is an instance of a known
// object, and noSuchObject otherwise.
func missing(tree []VarBind, name OID) Value {
	if len(name) > 1 {
		parent := name[:len(name)-1]
		i, _ := slices.BinarySearchFunc(tree, parent, func(vb VarBind, o OID) int { return vb.Name.Compare(o) })
		if i < len(tree) && len(tree[i].Name) == len(name) && tree[i].Name.HasPrefix(parent) {
			return Value{Tag: tagNoSuchInstance}
		}
	}
	return Value{Tag: tagNoSuchObject}
}

// --- tracking and traps ---

// watch keeps the alarm tables current and sends traps.
func (a *Agent) watch(ctx context.Context) {
	events := a.host.Subscribe(ctx, 64)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	a.refreshAll()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if u, ok := a.host.GetUPS(ev.UPS); ok {
				a.refresh(u)
			}
		case <-tick.C:
			a.refreshAll()
		}
	}
}

func (a *Agent) refreshAll() {
	for _, u := range a.host.ListUPS() {
		a.refresh(u)
	}
}

func (a *Agent) refresh(u sdk.UPS) {
	a.mu.Lock()
	_, traps := a.refreshLocked(u, a.now())
	a.mu.Unlock()
	for _, t := range traps {
		a.sendTrap(t)
	}
}

// refreshLocked updates the tracking of u and returns the traps due.
func (a *Agent) refreshLocked(u sdk.UPS, now time.Time) (*upsTrack, [][]VarBind) {
	t := a.track[u.Name]
	if t == nil {
		t = &upsTrack{}
		a.track[u.Name] = t
	}
	var traps [][]VarBind
	ident := VarBind{oidIdentName, stringValue(u.Name)}
	if u.State == sdk.StateConnected {
		t.everConnected = true
		ob := u.Status().Has(sdk.FlagOnBattery)
		if ob && !t.onBattery {
			t.onBattery, t.obSince = true, now
			t.lineBads++
		} else if !ob {
			t.onBattery = false
		}
		if ob && now.Sub(t.lastOBTrap) >= trapInterval {
			t.lastOBTrap = now
			vb := []VarBind{{oidUPSTraps.Append(1), Value{}}}
			if f, ok := u.Float("battery.runtime"); ok {
				vb = append(vb, VarBind{oidMinutesRemaining, intValue(nonNeg(f / 60))})
			}
			vb = append(vb, VarBind{oidSecondsOnBattery, intValue(t.secondsOnBattery(now))})
			if f, ok := u.Float("battery.runtime.low"); ok {
				vb = append(vb, VarBind{oidLowBattTime, intValue(nonNeg(f / 60))})
			}
			traps = append(traps, append(vb, ident))
		}
		if !ob {
			t.lastOBTrap = time.Time{}
		}
	}
	if !t.everConnected {
		return t, traps // still connecting at startup: no alarms yet
	}

	want := alarmsFor(u)
	var keep []alarmEntry
	for _, e := range t.alarms {
		if slices.Contains(want, e.descr) {
			keep = append(keep, e)
		} else {
			traps = append(traps, []VarBind{{oidUPSTraps.Append(4), Value{}},
				{oidAlarmID.Append(e.id), intValue(int64(e.id))},
				{oidAlarmDescr.Append(e.id), oidValue(oidWellKnown.Append(e.descr))}, ident})
		}
	}
	for _, d := range want {
		if slices.ContainsFunc(keep, func(e alarmEntry) bool { return e.descr == d }) {
			continue
		}
		e := alarmEntry{id: a.nextAlarm, descr: d, since: a.uptime(now)}
		a.nextAlarm++
		if a.nextAlarm > 0x7fffffff {
			a.nextAlarm = 1
		}
		keep = append(keep, e)
		traps = append(traps, []VarBind{{oidUPSTraps.Append(3), Value{}},
			{oidAlarmID.Append(e.id), intValue(int64(e.id))},
			{oidAlarmDescr.Append(e.id), oidValue(oidWellKnown.Append(e.descr))}, ident})
	}
	t.alarms = keep
	return t, traps
}

// sendTrap sends an SNMPv2c trap to every target. vbs[0].Name is the
// trap OID; its value is ignored.
func (a *Agent) sendTrap(vbs []VarBind) {
	a.mu.Lock()
	pc := a.conn
	a.mu.Unlock()
	if pc == nil || len(a.opts.Traps) == 0 {
		return
	}
	trap := vbs[0].Name
	list := append([]VarBind{
		{oidSysUpTime, ticksValue(a.uptime(a.now()))},
		{oidSnmpTrapOID, oidValue(trap)},
	}, vbs[1:]...)
	pdu := PDU{Type: pduTrapV2, RequestID: a.reqID.Add(1), VarBinds: list}
	for _, t := range a.opts.Traps {
		addr, err := net.ResolveUDPAddr("udp", t.Addr)
		if err != nil {
			a.log.Warn("snmp trap target", "target", t.Addr, "error", err)
			continue
		}
		var c []byte
		c = appendInt(c, 1)
		c = appendOctets(c, []byte(t.Community))
		c = append(c, pdu.encode()...)
		if _, err := pc.WriteTo(appendTLV(nil, tagSequence, c), addr); err != nil {
			a.log.Warn("snmp trap not sent", "target", t.Addr, "error", err)
		} else {
			a.log.Debug("snmp trap sent", "target", t.Addr, "trap", trap.String())
		}
	}
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
