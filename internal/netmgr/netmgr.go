// Package netmgr is a thin NetworkManager client over the system D-Bus. It is
// used only when NetworkManager is actually present on the host, so the whole
// feature (and its UI tab) auto-hides on distros/setups without it.
//
// Only a focused slice of the NM API is wired up: read status/devices/Wi-Fi/
// saved profiles, and the common actions (toggle radio, connect, disconnect,
// join a new Wi-Fi network, forget a profile).
package netmgr

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	nm        = "org.freedesktop.NetworkManager"
	nmPath    = "/org/freedesktop/NetworkManager"
	iNM       = "org.freedesktop.NetworkManager"
	iDevice   = "org.freedesktop.NetworkManager.Device"
	iWireless = "org.freedesktop.NetworkManager.Device.Wireless"
	iAP       = "org.freedesktop.NetworkManager.AccessPoint"
	iIP4      = "org.freedesktop.NetworkManager.IP4Config"
	iActive   = "org.freedesktop.NetworkManager.Connection.Active"
	iSettings = "org.freedesktop.NetworkManager.Settings"
	iConn     = "org.freedesktop.NetworkManager.Settings.Connection"
	settPath  = "/org/freedesktop/NetworkManager/Settings"
)

// Manager holds the system-bus connection. A nil *Manager means NM is absent.
type Manager struct {
	mu   sync.Mutex
	conn *dbus.Conn
}

// New connects to the system bus and verifies NetworkManager is answering.
// Returns nil if the bus or NM is unavailable (the caller then hides the tab).
func New() *Manager {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil
	}
	m := &Manager{conn: conn}
	// Probe: reading a core NM property must succeed.
	if _, err := m.prop(dbus.ObjectPath(nmPath), iNM, "Version"); err != nil {
		_ = conn.Close()
		return nil
	}
	return m
}

func (m *Manager) Available() bool { return m != nil && m.conn != nil }

// ---- data model (JSON-serialized to the browser) ---------------------------

type Data struct {
	Version           string      `json:"version"`
	State             string      `json:"state"`
	Connectivity      string      `json:"connectivity"`
	NetworkingEnabled bool        `json:"networkingEnabled"`
	WirelessEnabled   bool        `json:"wirelessEnabled"`
	WirelessHardware  bool        `json:"wirelessHardware"`
	Devices           []Device    `json:"devices"`
	WifiAPs           []AP        `json:"wifiAPs"`
	Saved             []SavedConn `json:"saved"`
}

type Device struct {
	Iface      string `json:"iface"`
	Type       string `json:"type"`
	State      string `json:"state"`
	IP4        string `json:"ip4"`
	ActiveConn string `json:"activeConn"`
}

type AP struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`
	Security string `json:"security"`
	Active   bool   `json:"active"`
	Iface    string `json:"iface"`
}

type SavedConn struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	Active bool   `json:"active"`
}

// ---- reads -----------------------------------------------------------------

// Snapshot gathers the full NM view. Safe for concurrent use.
func (m *Manager) Snapshot() (Data, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return Data{}, errors.New("networkmanager unavailable")
	}
	var d Data
	d.Version, _ = m.propStr(dbus.ObjectPath(nmPath), iNM, "Version")
	d.State = nmStateName(m.propU(dbus.ObjectPath(nmPath), iNM, "State"))
	d.Connectivity = connName(m.propU(dbus.ObjectPath(nmPath), iNM, "Connectivity"))
	d.NetworkingEnabled = m.propB(dbus.ObjectPath(nmPath), iNM, "NetworkingEnabled")
	d.WirelessEnabled = m.propB(dbus.ObjectPath(nmPath), iNM, "WirelessEnabled")
	d.WirelessHardware = m.propB(dbus.ObjectPath(nmPath), iNM, "WirelessHardwareEnabled")

	// active connection id per device path
	activeByDev := map[dbus.ObjectPath]string{}
	activeUUIDs := map[string]bool{}
	for _, ac := range m.propPaths(dbus.ObjectPath(nmPath), iNM, "ActiveConnections") {
		id, _ := m.propStr(ac, iActive, "Id")
		uuid, _ := m.propStr(ac, iActive, "Uuid")
		if uuid != "" {
			activeUUIDs[uuid] = true
		}
		for _, dp := range m.propPaths(ac, iActive, "Devices") {
			activeByDev[dp] = id
		}
	}

	devPaths := m.deviceList()
	for _, dp := range devPaths {
		iface, _ := m.propStr(dp, iDevice, "Interface")
		dtype := devTypeName(m.propU(dp, iDevice, "DeviceType"))
		if iface == "" || dtype == "loopback" {
			continue
		}
		dev := Device{
			Iface:      iface,
			Type:       dtype,
			State:      devStateName(m.propU(dp, iDevice, "State")),
			ActiveConn: activeByDev[dp],
			IP4:        m.deviceIP4(dp),
		}
		d.Devices = append(d.Devices, dev)

		if dtype == "wifi" {
			d.WifiAPs = append(d.WifiAPs, m.wifiAPs(dp, iface)...)
		}
	}
	sort.Slice(d.Devices, func(i, j int) bool { return d.Devices[i].Iface < d.Devices[j].Iface })

	// dedupe APs by SSID, keep strongest; active first
	best := map[string]AP{}
	for _, ap := range d.WifiAPs {
		if ap.SSID == "" {
			continue
		}
		if cur, ok := best[ap.SSID]; !ok || ap.Signal > cur.Signal || ap.Active {
			if ok && cur.Active {
				ap.Active = true
			}
			best[ap.SSID] = ap
		}
	}
	d.WifiAPs = d.WifiAPs[:0]
	for _, ap := range best {
		d.WifiAPs = append(d.WifiAPs, ap)
	}
	sort.Slice(d.WifiAPs, func(i, j int) bool {
		if d.WifiAPs[i].Active != d.WifiAPs[j].Active {
			return d.WifiAPs[i].Active
		}
		return d.WifiAPs[i].Signal > d.WifiAPs[j].Signal
	})

	d.Saved = m.savedConns(activeUUIDs)
	return d, nil
}

func (m *Manager) deviceList() []dbus.ObjectPath {
	var paths []dbus.ObjectPath
	err := m.conn.Object(nm, dbus.ObjectPath(nmPath)).
		Call(iNM+".GetDevices", 0).Store(&paths)
	if err != nil {
		return nil
	}
	return paths
}

func (m *Manager) deviceIP4(dp dbus.ObjectPath) string {
	ipPath := m.propPath(dp, iDevice, "Ip4Config")
	if ipPath == "" || ipPath == "/" {
		return ""
	}
	v, err := m.prop(ipPath, iIP4, "AddressData")
	if err != nil {
		return ""
	}
	list, ok := v.([]map[string]dbus.Variant)
	if !ok || len(list) == 0 {
		return ""
	}
	addr := ""
	prefix := ""
	if a, ok := list[0]["address"]; ok {
		addr, _ = a.Value().(string)
	}
	if p, ok := list[0]["prefix"]; ok {
		if pv, ok := p.Value().(uint32); ok {
			prefix = "/" + itoa(int(pv))
		}
	}
	if addr == "" {
		return ""
	}
	return addr + prefix
}

func (m *Manager) wifiAPs(devPath dbus.ObjectPath, iface string) []AP {
	activeAP := m.propPath(devPath, iWireless, "ActiveAccessPoint")
	var out []AP
	for _, ap := range m.propPaths(devPath, iWireless, "AccessPoints") {
		ssid := ""
		if v, err := m.prop(ap, iAP, "Ssid"); err == nil {
			if b, ok := v.([]byte); ok {
				ssid = string(b)
			}
		}
		if ssid == "" {
			continue
		}
		flags := m.propU(ap, iAP, "Flags")
		wpa := m.propU(ap, iAP, "WpaFlags")
		rsn := m.propU(ap, iAP, "RsnFlags")
		out = append(out, AP{
			SSID:     ssid,
			Signal:   int(m.propY(ap, iAP, "Strength")),
			Security: apSecurity(flags, wpa, rsn),
			Active:   ap == activeAP && activeAP != "/",
			Iface:    iface,
		})
	}
	return out
}

func (m *Manager) savedConns(activeUUIDs map[string]bool) []SavedConn {
	var paths []dbus.ObjectPath
	if err := m.conn.Object(nm, dbus.ObjectPath(settPath)).
		Call(iSettings+".ListConnections", 0).Store(&paths); err != nil {
		return nil
	}
	var out []SavedConn
	for _, p := range paths {
		var settings map[string]map[string]dbus.Variant
		if err := m.conn.Object(nm, p).Call(iConn+".GetSettings", 0).Store(&settings); err != nil {
			continue
		}
		c := settings["connection"]
		if c == nil {
			continue
		}
		id, _ := c["id"].Value().(string)
		typ, _ := c["type"].Value().(string)
		uuid, _ := c["uuid"].Value().(string)
		out = append(out, SavedConn{ID: id, Type: shortType(typ), UUID: uuid, Active: activeUUIDs[uuid]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ---- actions ---------------------------------------------------------------

// SetWireless toggles the Wi-Fi radio.
func (m *Manager) SetWireless(on bool) error {
	return m.setProp(dbus.ObjectPath(nmPath), iNM, "WirelessEnabled", dbus.MakeVariant(on))
}

// Disconnect deactivates whatever is on the named interface.
func (m *Manager) Disconnect(iface string) error {
	dp := m.deviceByIface(iface)
	if dp == "" {
		return errors.New("device not found")
	}
	return m.conn.Object(nm, dp).Call(iDevice+".Disconnect", 0).Err
}

// ActivateUUID brings up a saved connection by its UUID.
func (m *Manager) ActivateUUID(uuid string) error {
	cp := m.connByUUID(uuid)
	if cp == "" {
		return errors.New("connection not found")
	}
	return m.conn.Object(nm, dbus.ObjectPath(nmPath)).
		Call(iNM+".ActivateConnection", 0, cp, dbus.ObjectPath("/"), dbus.ObjectPath("/")).Err
}

// DeleteUUID forgets a saved connection.
func (m *Manager) DeleteUUID(uuid string) error {
	cp := m.connByUUID(uuid)
	if cp == "" {
		return errors.New("connection not found")
	}
	return m.conn.Object(nm, cp).Call(iConn+".Delete", 0).Err
}

// ConnectWifi joins an SSID, creating+activating a profile. psk may be empty
// for open networks.
func (m *Manager) ConnectWifi(ssid, psk string) error {
	// Reuse an existing saved profile if present.
	if uuid := m.wifiUUIDBySSID(ssid); uuid != "" {
		return m.ActivateUUID(uuid)
	}
	dp := m.firstWifiDevice()
	if dp == "" {
		return errors.New("no wifi device")
	}
	wireless := map[string]dbus.Variant{
		"ssid": dbus.MakeVariant([]byte(ssid)),
		"mode": dbus.MakeVariant("infrastructure"),
	}
	settings := map[string]map[string]dbus.Variant{
		"connection": {
			"id":   dbus.MakeVariant(ssid),
			"type": dbus.MakeVariant("802-11-wireless"),
		},
		"802-11-wireless": wireless,
		"ipv4":            {"method": dbus.MakeVariant("auto")},
		"ipv6":            {"method": dbus.MakeVariant("auto")},
	}
	if psk != "" {
		settings["802-11-wireless-security"] = map[string]dbus.Variant{
			"key-mgmt": dbus.MakeVariant("wpa-psk"),
			"psk":      dbus.MakeVariant(psk),
		}
	}
	apPath := m.apPathBySSID(dp, ssid)
	if apPath == "" {
		apPath = "/"
	}
	return m.conn.Object(nm, dbus.ObjectPath(nmPath)).
		Call(iNM+".AddAndActivateConnection", 0, settings, dp, apPath).Err
}

// ---- lookup helpers --------------------------------------------------------

func (m *Manager) deviceByIface(iface string) dbus.ObjectPath {
	for _, dp := range m.deviceList() {
		if s, _ := m.propStr(dp, iDevice, "Interface"); s == iface {
			return dp
		}
	}
	return ""
}

func (m *Manager) firstWifiDevice() dbus.ObjectPath {
	for _, dp := range m.deviceList() {
		if devTypeName(m.propU(dp, iDevice, "DeviceType")) == "wifi" {
			return dp
		}
	}
	return ""
}

func (m *Manager) connByUUID(uuid string) dbus.ObjectPath {
	var p dbus.ObjectPath
	err := m.conn.Object(nm, dbus.ObjectPath(settPath)).
		Call(iSettings+".GetConnectionByUuid", 0, uuid).Store(&p)
	if err != nil {
		return ""
	}
	return p
}

func (m *Manager) wifiUUIDBySSID(ssid string) string {
	var paths []dbus.ObjectPath
	if err := m.conn.Object(nm, dbus.ObjectPath(settPath)).
		Call(iSettings+".ListConnections", 0).Store(&paths); err != nil {
		return ""
	}
	for _, p := range paths {
		var s map[string]map[string]dbus.Variant
		if err := m.conn.Object(nm, p).Call(iConn+".GetSettings", 0).Store(&s); err != nil {
			continue
		}
		if w := s["802-11-wireless"]; w != nil {
			if b, ok := w["ssid"].Value().([]byte); ok && string(b) == ssid {
				uuid, _ := s["connection"]["uuid"].Value().(string)
				return uuid
			}
		}
	}
	return ""
}

func (m *Manager) apPathBySSID(devPath dbus.ObjectPath, ssid string) dbus.ObjectPath {
	for _, ap := range m.propPaths(devPath, iWireless, "AccessPoints") {
		if v, err := m.prop(ap, iAP, "Ssid"); err == nil {
			if b, ok := v.([]byte); ok && string(b) == ssid {
				return ap
			}
		}
	}
	return ""
}

// ---- low-level property access --------------------------------------------

func (m *Manager) prop(path dbus.ObjectPath, iface, name string) (interface{}, error) {
	v, err := m.conn.Object(nm, path).GetProperty(iface + "." + name)
	if err != nil {
		return nil, err
	}
	return v.Value(), nil
}

func (m *Manager) setProp(path dbus.ObjectPath, iface, name string, val dbus.Variant) error {
	return m.conn.Object(nm, path).
		Call("org.freedesktop.DBus.Properties.Set", 0, iface, name, val).Err
}

func (m *Manager) propStr(p dbus.ObjectPath, i, n string) (string, error) {
	v, err := m.prop(p, i, n)
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	return s, nil
}
func (m *Manager) propB(p dbus.ObjectPath, i, n string) bool {
	v, _ := m.prop(p, i, n)
	b, _ := v.(bool)
	return b
}
func (m *Manager) propU(p dbus.ObjectPath, i, n string) uint32 {
	v, _ := m.prop(p, i, n)
	u, _ := v.(uint32)
	return u
}
func (m *Manager) propY(p dbus.ObjectPath, i, n string) uint8 {
	v, _ := m.prop(p, i, n)
	y, _ := v.(uint8)
	return y
}
func (m *Manager) propPath(p dbus.ObjectPath, i, n string) dbus.ObjectPath {
	v, _ := m.prop(p, i, n)
	op, _ := v.(dbus.ObjectPath)
	return op
}
func (m *Manager) propPaths(p dbus.ObjectPath, i, n string) []dbus.ObjectPath {
	v, _ := m.prop(p, i, n)
	ops, _ := v.([]dbus.ObjectPath)
	return ops
}

// ---- enum → string ---------------------------------------------------------

func nmStateName(s uint32) string {
	switch s {
	case 70:
		return "connected"
	case 60:
		return "connected (site)"
	case 50:
		return "connected (local)"
	case 40:
		return "connecting"
	case 30:
		return "disconnecting"
	case 20:
		return "disconnected"
	case 10:
		return "asleep"
	}
	return "unknown"
}
func connName(c uint32) string {
	switch c {
	case 4:
		return "full"
	case 3:
		return "limited"
	case 2:
		return "portal"
	case 1:
		return "none"
	}
	return "unknown"
}
func devTypeName(t uint32) string {
	switch t {
	case 1:
		return "ethernet"
	case 2:
		return "wifi"
	case 5:
		return "bluetooth"
	case 6:
		return "wimax"
	case 32:
		return "loopback"
	}
	return "other"
}
func devStateName(s uint32) string {
	switch {
	case s == 100:
		return "connected"
	case s == 30:
		return "disconnected"
	case s == 20:
		return "unavailable"
	case s == 10:
		return "unmanaged"
	case s == 120:
		return "failed"
	case s >= 40 && s <= 90:
		return "connecting"
	case s == 110:
		return "deactivating"
	}
	return "unknown"
}
func apSecurity(flags, wpa, rsn uint32) string {
	if rsn != 0 {
		return "WPA2/3"
	}
	if wpa != 0 {
		return "WPA"
	}
	if flags&0x1 != 0 {
		return "WEP"
	}
	return "Open"
}
func shortType(t string) string {
	t = strings.TrimPrefix(t, "802-11-")
	t = strings.TrimPrefix(t, "802-3-")
	return t
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
