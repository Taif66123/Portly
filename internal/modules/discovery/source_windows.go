package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"unsafe"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// iphlpapi table classes
const (
	tcpOwnerModuleListener = 6
	udpOwnerModule         = 2
	systemPID              = 4
	maxTableAttempts       = 8
	firstEphemeral         = 49152
	lastEphemeral          = 65535
)

var (
	iphlpapi      = windows.NewLazySystemDLL("iphlpapi.dll")
	getTCPTable   = iphlpapi.NewProc("GetExtendedTcpTable")
	getUDPTable   = iphlpapi.NewProc("GetExtendedUdpTable")
	checkDebugger = windows.NewLazySystemDLL("kernel32.dll").NewProc("CheckRemoteDebuggerPresent")
)

var errGone = errors.New("process is gone")

// source reads Windows through iphlpapi and process handles.
type source struct {
	users *accounts
}

// newSource reads the real system.
func newSource() source {
	dropDebugPrivilege()
	return source{users: &accounts{}}
}

// dropDebugPrivilege undoes what importing gopsutil enables for administrators.
func dropDebugPrivilege() {
	var token windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES, &token) != nil {
		return
	}
	defer token.Close()
	privileges := windows.Tokenprivileges{PrivilegeCount: 1}
	name, err := windows.UTF16PtrFromString("SeDebugPrivilege")
	if err == nil && windows.LookupPrivilegeValue(nil, name, &privileges.Privileges[0].Luid) == nil {
		_ = windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil)
	}
}

// scan joins socket tables with processes that are older than their sockets.
func (s source) scan(ctx context.Context) (domain.Snapshot, error) {
	found, tables, err := readSockets()
	if err != nil {
		return domain.Snapshot{}, err
	}
	tree, err := readTree()
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("list processes: %w", err)
	}
	services, isSCMKnown := readServices()
	snapshot := domain.Snapshot{Host: host(), Processes: map[int]domain.Process{}}
	for pid := range tree {
		snapshot.Processes[pid] = basicProcess(pid, tree)
	}
	s.addDetails(ctx, &snapshot, found, func(pid int) domain.Tri { return serviceState(pid, tree, services, isSCMKnown) })
	for _, socket := range found {
		snapshot.Sockets = append(snapshot.Sockets, attribute(snapshot, socket, tables))
	}
	snapshot.Ports = portStates(snapshot.Sockets, tables)
	if ctx.Err() != nil {
		snapshot.Host.ProcessTable = domain.Partial
		snapshot.Warnings = append(snapshot.Warnings, "The scan timed out, results are partial")
	}
	return snapshot, nil
}

// host reads facts about this machine and caller.
func host() domain.Host {
	return domain.Host{
		SelfPID: os.Getpid(), CallerUID: tokenUser(windows.CurrentProcess()), Privileged: windows.GetCurrentProcessToken().IsElevated(),
		InContainer: domain.No, ServiceManager: domain.Yes, ProcessTable: domain.Complete,
		Ephemeral: domain.Range{First: firstEphemeral, Last: lastEphemeral},
	}
}

// attribute links a socket to its recorded owner, if that owner can be the real one.
func attribute(snapshot domain.Snapshot, socket winSocket, tables map[domain.Protocol]domain.Completeness) domain.Socket {
	ownership := domain.Unavailable
	owner, known := snapshot.Processes[socket.pid]
	started, err := strconv.ParseUint(owner.Start, 10, 64)
	if known && err == nil && ownsSince(started, socket.created) {
		socket.Holders, socket.CreatorUID = []domain.Holder{{PID: socket.pid, FD: -1}}, owner.UserID
		ownership = domain.Partial
	}
	socket.Evidence = []domain.Evidence{
		{Source: "iphlpapi", Completeness: tables[socket.ID.Protocol]}, {Source: "bind-time", Completeness: ownership},
	}
	return socket.Socket
}

// readSockets reads the four OWNER_MODULE tables. IPv6 may be missing.
func readSockets() ([]winSocket, map[domain.Protocol]domain.Completeness, error) {
	tables := []struct {
		call   *windows.LazyProc
		family uintptr
		class  uintptr
		layout rowLayout
	}{
		{getTCPTable, windows.AF_INET, tcpOwnerModuleListener, tcp4Row}, {getTCPTable, windows.AF_INET6, tcpOwnerModuleListener, tcp6Row},
		{getUDPTable, windows.AF_INET, udpOwnerModule, udp4Row}, {getUDPTable, windows.AF_INET6, udpOwnerModule, udp6Row},
	}
	completeness := map[domain.Protocol]domain.Completeness{domain.TCP: domain.Complete, domain.UDP: domain.Complete}
	var sockets []winSocket
	for _, table := range tables {
		data, err := readTable(table.call, table.family, table.class)
		if err != nil && table.family == windows.AF_INET {
			return nil, nil, fmt.Errorf("read socket table: %w", err)
		}
		if errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
			continue
		}
		found, quality := parseWindowsTable(data, table.layout)
		sockets = append(sockets, found...)
		completeness[table.layout.protocol] = min(completeness[table.layout.protocol], quality)
	}
	return sockets, completeness, nil
}

// readTable grows the buffer until the table fits.
func readTable(call *windows.LazyProc, family, class uintptr) ([]byte, error) {
	if err := call.Find(); err != nil {
		return nil, err
	}
	size := uint32(16 << 10)
	for range maxTableAttempts {
		// uint64 backing keeps the buffer 8-byte aligned
		buffer := make([]uint64, size/8+1)
		result, _, _ := call.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, family, class, 0)
		switch windows.Errno(result) {
		case windows.ERROR_SUCCESS:
			return unsafe.Slice((*byte)(unsafe.Pointer(&buffer[0])), len(buffer)*8), nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			size += 4 << 10
		default:
			return nil, windows.Errno(result)
		}
	}
	return nil, windows.ERROR_INSUFFICIENT_BUFFER
}

// treeEntry is one process in the system's own list.
type treeEntry struct {
	parent  int
	name    string
	start   uint64
	session uint32
}

// readTree lists every process in one call, without opening any of them.
func readTree() (map[int]treeEntry, error) {
	size := uint32(1 << 20)
	for range maxTableAttempts {
		// uint64 backing keeps the buffer 8-byte aligned
		buffer := make([]uint64, size/8)
		var needed uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buffer[0]), size, &needed)
		if errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) {
			size = max(size*2, needed+64<<10)
			continue
		}
		if err != nil {
			return nil, err
		}
		return parseTree(unsafe.Pointer(&buffer[0]), uintptr(size)), nil
	}
	return nil, windows.STATUS_INFO_LENGTH_MISMATCH
}

// parseTree walks the linked records inside the buffer.
func parseTree(base unsafe.Pointer, size uintptr) map[int]treeEntry {
	tree := map[int]treeEntry{}
	for offset := uintptr(0); offset+unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{}) <= size; {
		entry := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Add(base, offset))
		tree[int(entry.UniqueProcessID)] = treeEntry{
			parent: int(entry.InheritedFromUniqueProcessID), name: entry.ImageName.String(),
			start: uint64(entry.CreateTime), session: entry.SessionID,
		}
		if entry.NextEntryOffset == 0 {
			break
		}
		offset += uintptr(entry.NextEntryOffset)
	}
	return tree
}

// basicProcess fills what does not need a handle. Windows has no signals.
func basicProcess(pid int, tree map[int]treeEntry) domain.Process {
	entry := tree[pid]
	// Partial: the tables name only the process that created a socket, never children that inherited it
	found := domain.Process{
		Name: entry.name, Kernel: pid <= systemPID, Sockets: domain.Partial, TTY: domain.Unknown,
		Caught: domain.No, Ignored: domain.No, Blocked: domain.No,
		Unit: domain.Unit{Kind: domain.UnitNone}, Job: domain.Job{Known: true},
	}
	found.PID = pid
	if entry.start != 0 {
		found.Start = strconv.FormatUint(entry.start, 10)
	}
	// A parent ID may have been reused by a younger process
	if parent, known := tree[entry.parent]; known && parent.start < entry.start {
		found.PPID = entry.parent
	}
	return found
}

// openQuery opens a process for reading basic facts.
func openQuery(pid int) (windows.Handle, error) {
	return windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
}

// addDetails reads owners, their ancestors and Portly fully.
func (s source) addDetails(ctx context.Context, snapshot *domain.Snapshot, sockets []winSocket, service func(int) domain.Tri) {
	relevant := map[int]bool{}
	mark := func(pid int) {
		relevant[pid] = true
		ancestors, _ := snapshot.Ancestors(pid)
		for _, ancestor := range ancestors {
			relevant[ancestor.PID] = true
		}
	}
	mark(snapshot.Host.SelfPID)
	for _, socket := range sockets {
		mark(socket.pid)
	}
	for pid := range relevant {
		found, known := snapshot.Processes[pid]
		if !known || ctx.Err() != nil {
			continue
		}
		found.Service = service(pid)
		snapshot.Processes[pid] = s.detail(ctx, found)
	}
}

// detail reads one process through its handle. Unreadable fields stay empty.
func (s source) detail(ctx context.Context, found domain.Process) domain.Process {
	handle, err := openQuery(found.PID)
	if err != nil {
		return found
	}
	defer windows.CloseHandle(handle)
	if startTime(handle) != found.Start {
		return found
	}
	found.Exe, found.UserID = imagePath(handle), tokenUser(handle)
	found.RealUID = found.UserID
	owner := s.users.lookup(found.UserID)
	found.User, found.Home = owner.name, owner.home
	found.Tracer = debuggerState(found.PID)
	details := &process.Process{Pid: int32(found.PID)}
	found.Args, _ = details.CmdlineSliceWithContext(ctx)
	found.Dir, _ = details.CwdWithContext(ctx)
	return found
}

// startTime is the creation time in 100 ns, empty when unknown.
func startTime(handle windows.Handle) string {
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &created, &exited, &kernel, &user) != nil {
		return ""
	}
	value := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if value == 0 {
		return ""
	}
	return strconv.FormatUint(value, 10)
}

// imagePath is the full executable path.
func imagePath(handle windows.Handle) string {
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size) != nil {
		return ""
	}
	return windows.UTF16ToString(buffer[:size])
}

// tokenUser is the user's SID, empty when unreadable.
func tokenUser(handle windows.Handle) string {
	var token windows.Token
	if windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token) != nil {
		return ""
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return ""
	}
	return user.User.Sid.String()
}

// debuggerState is unknown when the process can't be asked.
func debuggerState(pid int) domain.Tri {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return domain.Unknown
	}
	defer windows.CloseHandle(handle)
	if checkDebugger.Find() != nil {
		return domain.Unknown
	}
	var isPresent int32
	result, _, _ := checkDebugger.Call(uintptr(handle), uintptr(unsafe.Pointer(&isPresent)))
	if result == 0 {
		return domain.Unknown
	}
	return domain.TriOf(isPresent != 0)
}

// serviceState covers services and session 0, unknown without the service manager.
func serviceState(pid int, tree map[int]treeEntry, services map[int]string, isSCMKnown bool) domain.Tri {
	switch {
	case services[pid] != "":
		return domain.Yes
	case !isSCMKnown:
		return domain.Unknown
	}
	return domain.TriOf(tree[pid].session == 0)
}

// readServices maps PIDs to service names, false when it can't be read.
func readServices() (map[int]string, bool) {
	services := map[int]string{}
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return services, false
	}
	defer windows.CloseServiceHandle(manager)
	size := uint32(256 << 10)
	for range maxTableAttempts {
		buffer := make([]uint64, size/8)
		var needed, count uint32
		err = windows.EnumServicesStatusEx(manager, windows.SC_ENUM_PROCESS_INFO, windows.SERVICE_WIN32, windows.SERVICE_ACTIVE,
			(*byte)(unsafe.Pointer(&buffer[0])), size, &needed, &count, nil, nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			size += needed + 4<<10
			continue
		}
		if err != nil {
			return services, false
		}
		for _, status := range unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buffer[0])), count) {
			services[int(status.ServiceStatusProcess.ProcessId)] = windows.UTF16PtrToString(status.ServiceName)
		}
		return services, true
	}
	return services, false
}

// addManagers has nothing to add, services are read during the scan.
func (source) addManagers(context.Context, *domain.Snapshot, []domain.Endpoint) {}

// reread finds the socket first and reads identity last, right before the kill.
func (s source) reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error) {
	sockets, _, err := readSockets()
	if err != nil {
		return domain.Process{}, false, err
	}
	holds := false
	for _, socket := range sockets {
		holds = holds || socket.pid == holder.PID && socket.ID.Same(id)
	}
	handle, err := openQuery(holder.PID)
	if err != nil {
		return domain.Process{}, false, fmt.Errorf("open process %d: %w", holder.PID, errGone)
	}
	defer windows.CloseHandle(handle)
	found := domain.Process{Tracer: debuggerState(holder.PID)}
	found.ProcessID = domain.ProcessID{PID: holder.PID, Start: startTime(handle), UserID: tokenUser(handle), Exe: imagePath(handle)}
	return found, holds, nil
}
