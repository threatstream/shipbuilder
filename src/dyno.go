package main

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jaytaylor/streamon"
)

// TODO: ENSURE ALLOCATIONS TIMEOUT.

type (
	Dyno struct {
		Host, Container, Application, Process, Version, Port, State string
		VersionNumber, PortNumber                                   int
		LastUpdatedTs                                               time.Time
	}

	// lxcMonitorQuitChannel and memoryMonitorQuitChannel members: When these
	// are nil it signals that monitoring is stopped, and when they are not
	// nil, monitoring must be active.
	NodeStatus struct {
		Host                                            string
		FreeMemoryMb                                    int
		Dynos                                           map[string]*Dyno
		DeployMarker                                    int
		Ts                                              time.Time
		SystemDynoState                                 *SystemDynoState
		Error                                           error
		lock                                            sync.Mutex
		lxcMonitorQuitChannel, memoryMonitorQuitChannel chan bool
	}

	NodeStatusRunning struct {
		status  NodeStatus
		running bool
	}

	NodeStatuses []NodeStatusRunning

	DynoGenerator struct {
		server      *Server
		statuses    []NodeStatusRunning
		position    int
		application string
		version     string
	}

	DynoPortTracker struct {
		allocations map[string][]int
		lock        sync.Mutex
	}

	SystemDynoState struct {
		NodeStates     map[string]NodeStatus
		NodeStatesLock sync.Mutex
	}
)

const (
	DYNO_DELIMITER       = "_"
	DYNO_STATE_ALLOCATED = "ALLOCATED"
	DYNO_STATE_STARTING  = "STARTING"
	DYNO_STATE_RUNNING   = "RUNNING"
	DYNO_STATE_STOPPED   = "STOPPED"
	DYNO_STATE_STOPPING  = "STOPPING"
)

var (
	//dynoPortTracker = DynoPortTracker{allocations: map[string][]int{}, lock: sync.Mutex{}}

	dynoStateParserRe  = regexp.MustCompile(`'([^']+) changed state to \[([^\]]+)\]'`)
	freeMemoryParserRe = regexp.MustCompile(`[0-9]+`)
)

func (this *SystemDynoState) NewNodeState(host string) *NodeStatus {
	nodeState := &NodeStatus{
		Host:            strings.ToLower(host),
		FreeMemoryMb:    -1,
		Dynos:           map[string]*Dyno{},
		DeployMarker:    -1,
		SystemDynoState: this,
		Ts:              time.Now(),
		Error:           nil,
	}
	return nodeState
}

// Attempt to parse a container string into a Dyno struct.  This is very, ahem, "forgiving".
// Container name format is: appName_version_process_port[_state?]
//
// NB: State is only overridden by the container string when an empty state string is passed in initially.
func ContainerToDyno(host, container, state string) *Dyno {
	log.Printf("container=%v\n", container)
	version := "0"
	var versionNumber int
	process := ""
	port := "0"
	var portNumber int
	tokens := strings.Split(container, DYNO_DELIMITER)
	application := tokens[0]
	if len(tokens) >= 2 {
		version = tokens[1]
	}
	if len(tokens) >= 3 {
		process = tokens[2]
	}
	if len(tokens) >= 4 {
		port = tokens[3]
	}
	// NB: State is only overridden by the container string when an empty state string is passed in initially.
	if len(tokens) >= 5 && state == "" {
		state = tokens[4]
	}
	versionNumber, err := strconv.Atoi(strings.TrimPrefix(version, "v"))
	if err != nil {
		versionNumber = 0
	}
	portNumber, err = strconv.Atoi(port)
	if err != nil {
		portNumber = 0
	}
	return &Dyno{
		Host:          host,
		Container:     container,
		Application:   application,
		Version:       version,
		Process:       process,
		Port:          port,
		State:         state,
		VersionNumber: versionNumber,
		PortNumber:    portNumber,
	}
}

func (this *Dyno) Info() string {
	return fmt.Sprintf("host=%v app=%v version=%v proc=%v port=%v state=%v", this.Host, this.Application, this.Version, this.Process, this.Port, this.State)
}

func (this *Dyno) Shutdown(e *Executor) error {
	fmt.Fprintf(e.logger, "Shutting down dyno: %v\n", this.Info())
	if this.State == DYNO_STATE_RUNNING {
		// Shutdown then destroy.
		return e.Run("ssh", DEFAULT_NODE_USERNAME+"@"+this.Host, "sudo", "/tmp/shutdown_container.py", this.Container)
	} else {
		// Destroy only.
		return e.Run("ssh", DEFAULT_NODE_USERNAME+"@"+this.Host, "sudo", "/tmp/shutdown_container.py", this.Container, "destroy-only")
	}
}

func (this *Dyno) AttachAndExecute(e *Executor, args ...string) error {
	// If the Dyno isn't running we won't be able to attach to it.
	if this.State != DYNO_STATE_RUNNING {
		return fmt.Errorf("can't run `%v` when dyno is not running, details: %v", args, this.Info())
	}
	args = AppendStrings([]string{DEFAULT_NODE_USERNAME + "@" + this.Host, "sudo", "lxc-attach", "-n", this.Container, "--"}, args...)
	return e.Run("ssh", args...)
}

func (this *Dyno) RestartService(e *Executor) error {
	fmt.Fprintf(e.logger, "Restarting app service for dyno %v\n", this.Info())
	return this.AttachAndExecute(e, "service", "app", "restart")
}

func (this *Dyno) StartService(e *Executor) error {
	fmt.Fprintf(e.logger, "Starting app service for dyno %v\n", this.Info())
	return this.AttachAndExecute(e, "service", "app", "start")
}

func (this *Dyno) StopService(e *Executor) error {
	fmt.Fprintf(e.logger, "Stopping app service for dyno %v\n", this.Info())
	return this.AttachAndExecute(e, "service", "app", "stop")
}

func (this *Dyno) GetServiceStatus(e *Executor) error {
	fmt.Fprintf(e.logger, "Getting app service status for dyno %v\n", this.Info())
	return this.AttachAndExecute(e, "service", "app", "status")
}

func NewSystemDynoState() *SystemDynoState {
	return &SystemDynoState{
		NodeStates:     map[string]NodeStatus{},
		NodeStatesLock: sync.Mutex{},
	}
}

// Helper method to automatically handle locking for methods that have a common signature.
func (this *SystemDynoState) AutoLockFn(fn func() error) error {
	this.NodeStatesLock.Lock()
	defer this.NodeStatesLock.Unlock()
	return fn()
}

// Helper method to automatically handle locking and hostname lowercase conversion for methods that have a common signature.
func (this *SystemDynoState) AutoLockHostFn(host string, fn func(string) error) error {
	this.NodeStatesLock.Lock()
	defer this.NodeStatesLock.Unlock()
	host = strings.ToLower(host) // Guard against casing dupes/issues.
	return fn(host)
}

func (this *NodeStatus) StartMonitoring() error {
	this.lock.Lock()
	this.lock.Unlock()
	if this.lxcMonitorQuitChannel != nil {
		return fmt.Errorf("NodeStatus.StartMonitoring: host=%v illegal operation when lxcMonitorQuitChannel is not nil", this.Host)
	}
	if this.memoryMonitorQuitChannel != nil {
		return fmt.Errorf("NodeStatus.StartMonitoring: host=%v illegal operation when memoryMonitorQuitChannel is not nil", this.Host)
	}
	this.lxcMonitorQuitChannel = make(chan bool)
	this.memoryMonitorQuitChannel = make(chan bool)
	return nil
}

func (this *NodeStatus) StopMonitoring() error {
	this.lock.Lock()
	this.lock.Unlock()
	if this.lxcMonitorQuitChannel == nil {
		return fmt.Errorf("NodeStatus.StopMonitoring: host=%v illegal operation when lxcMonitorQuitChannel is not nil", this.Host)
	}
	if this.memoryMonitorQuitChannel == nil {
		return fmt.Errorf("NodeStatus.StopMonitoring: host=%v illegal operation when memoryMonitorQuitChannel is not nil", this.Host)
	}
	this.lxcMonitorQuitChannel <- true
	close(this.lxcMonitorQuitChannel)
	this.lxcMonitorQuitChannel = nil
	this.memoryMonitorQuitChannel <- true
	close(this.memoryMonitorQuitChannel)
	this.memoryMonitorQuitChannel = nil
	return nil
}

func quittableStreamAttachLoop(commandArgs []string, re *regexp.Regexp, quit <-chan bool, matchCb func([]string)) {
	for {
		commandListener, err := streamon.NewCommandListener(commandArgs, re)
		if err != nil {
			panic(err)
		}
		ch := make(chan []string)
		commandListener.Attach(ch)
		for ch != nil {
			select {
			case match, ok := <-ch:
				if !ok {
					ch = nil
				}
				matchCb(match)

			case <-quit:
				log.Printf("quittableStreamAttachLoop: quit message received for commandArgs=%v, goroutine terminating\n", commandArgs)
				return
			}
		}
	}
}

// TODO: plug this into node add/remove
func (this *SystemDynoState) monitorMemory(nodeState NodeStatus) {
	frontArgs := PersistentSshCommand(nodeState.Host)
	commandArgs := append(frontArgs, MEMORY_MONITOR_COMMAND)
	cb := func(match []string) {
		freeMemoryMb, err := strconv.Atoi(match[0])
		if err != nil {
			log.Printf("SystemDynoState.monitorNodeMemory: error parsing value=%v to an integer: %v\n", match[0], err)
			return
		}
		this.ProcessFreeMemoryUpdate(nodeState.Host, freeMemoryMb)
	}
	log.Printf("SystemDynoState.monitorNodeMemory: starting memory monitor for host=%v\n", nodeState.Host)
	quittableStreamAttachLoop(commandArgs, freeMemoryParserRe, nodeState.memoryMonitorQuitChannel, cb)
}

// TODO: plug this into node add/remove
// Continually loops the monitoring upon interruption until the a quit channel message is received.
func (this *SystemDynoState) monitorHostDynos(nodeState NodeStatus) {
	frontArgs := PersistentSshCommand(nodeState.Host)
	commandArgs := append(frontArgs, LXC_STATE_MONITOR_COMMAND)
	cb := func(match []string) {
		this.ProcessDynoUpdate(nodeState.Host, match[1], match[2])
	}
	log.Printf("SystemDynoState.monitorHostDynos: starting dyno monitor for host=%v\n", nodeState.Host)
	quittableStreamAttachLoop(commandArgs, dynoStateParserRe, nodeState.lxcMonitorQuitChannel, cb)
}

// Get or initialize a new NodeStatus for the given host.
// If a new NodeStatus is created, monitoring will automatically be started.
//
// Not thread-safe (invoker is responsible for locking appropriately).
func (this *SystemDynoState) getOrInitNodeState(host string) *NodeStatus {
	nodeState, ok := this.NodeStates[host]
	if !ok {
		newNodeState := this.NewNodeState(host)
		err := newNodeState.StartMonitoring()
		if err != nil {
			log.Printf("SystemDynoState.getOrInitNodeState: error starting monitoring for host=%v errmsg=%v\n", host, err)
		}
		log.Printf("SystemDynoState.getOrInitNodeState: Registering new host=%v\n", host)
		this.NodeStates[host] = *newNodeState
		nodeState = *newNodeState
	}
	return &nodeState
}

// Intended for use when the addition of a new SB Node happens.
func (this *SystemDynoState) RegisterHost(host string) {
	this.AutoLockHostFn(host, func(host string) error {
		this.getOrInitNodeState(host)
		return nil
	})
}

// Intended for use when the deletion of a new SB Node happens.
func (this *SystemDynoState) RemoveHost(host string) {
	this.AutoLockHostFn(host, func(host string) error {
		if nodeState, ok := this.NodeStates[host]; ok {
			err := nodeState.StopMonitoring()
			if err != nil {
				log.Printf("SystemDynoState.RemoveHost: error stopping monitoring for host=%v errmsg=%v\n", host, err)
				return err
			}
			delete(this.NodeStates, host)
		}
		return nil
	})
}

// // Returns array of ports in-use by Dynos.
// //
// // Not thread-safe (invoker is responsible for locking appropriately).
// func (this *SystemDynoState) getDynoPorts() []int {
// 	used := []int{}
// 	for _, nodeState := range this.NodeStates {
// 		for _, dyno := range nodeState.Dynos {
// 			used = append(used, dyno.PortNumber)
// 		}
// 	}
// 	return used
// }

// // Returns array of reserved port numbers.
// //
// // Not thread-safe (invoker is responsible for locking appropriately).
// func (this *SystemDynoState) getReservedPorts() []int {
// 	used := []int{}
// 	for port, _ := range this.PortReservations {
// 		used = append(used, port)
// 	}
// 	return used
// }

// // Returns both the ports in use by dynos as well as port reservations.
// func (this *SystemDynoState) GetAllUsedPorts() []int {
// 	this.portReservationsLock.Lock()
// 	defer this.portReservationsLock.Unlock()
// 	this.NodeStatesLock.Lock()
// 	defer this.NodeStatesLock.Unlock()
// 	used := append(this.getDynoPorts(), this.getReservedPorts()...)
// 	return used
// }

// // Add a port reservation.
// func (this *SystemDynoState) ReservePort(port int) error {
// 	if port < DYNO_PORT_ALLOCATION_START || port > DYNO_PORT_ALLOCATION_END {
// 		return fmt.Errorf("SystemDynoState.ReservePort :: requested port=%v not in allowed range of %v-%v", port, DYNO_PORT_ALLOCATION_START, DYNO_PORT_ALLOCATION_END)
// 	}

// 	this.portReservationsLock.Lock()
// 	defer this.portReservationsLock.Unlock()
// 	if ts, ok := this.PortReservations[port]; ok {
// 		return fmt.Errorf("SystemDynoState.ReservePort :: port %v was already reserved since %v ago", time.Since(ts).String())
// 	}

// 	this.NodeStatesLock.Lock()
// 	defer this.NodeStatesLock.Unlock()
// 	for _, dynoPort := range this.getDynoPorts() {
// 		if port == dynoPort {
// 			return fmt.Errorf("SystemDynoState.ReservePort :: port %v is already in use for an active dyno")
// 		}
// 	}

// 	// Add the port reservation.
// 	this.PortReservations[port] = time.Now()

// 	// Add automatic expiration.
// 	go func(port int) {
// 		for {
// 			select {
// 			case <-time.After(DYNO_PORT_ALLOCATION_EXPIRY_SECONDS * time.Second):
// 				this.ClearPortReservationIfExists(port)
// 				return
// 			}
// 		}
// 	}(port)

// 	return nil
// }

// // Clear a port reservation.
// func (this *SystemDynoState) ClearPortReservationIfExists(port int) {
// 	this.portReservationsLock.Lock()
// 	defer this.portReservationsLock.Unlock()
// 	if ts, ok := this.PortReservations[port]; ok {
// 		log.Printf("ClearPortReservationIfExists :: Clearing port reservation for port=%v which was reserved %s ago\n", port, time.Since(ts).String())
// 		delete(this.PortReservations, port)
// 	}
// }

// Process an update (these originate from `lxc-monitor '.*'`, mutate state accordingly.
func (this *SystemDynoState) ProcessDynoUpdate(host, container, state string) error {
	log.Printf("SystemDynoState.ProcessDynoUpdate: update received host=%v container=%v state=%v\n", host, container, state)
	return this.AutoLockHostFn(host, func(host string) error {
		// Get node state for specified host.
		var nodeState NodeStatus
		if nodeState, ok := this.NodeStates[host]; !ok {
			// Add new node state.
			newNodeState := this.NewNodeState(host)
			nodeState = *newNodeState
			this.NodeStates[host] = nodeState
		}
		dynoPtr := ContainerToDyno(host, container, state)
		if state == DYNO_STATE_ALLOCATED {
			// Ensure there are no port conflicts.
			for _, existingDyno := range nodeState.Dynos {
				if existingDyno.PortNumber == dynoPtr.PortNumber {
					return fmt.Errorf("SystemDynoState.ProcessDynoUpdate: port=%v already in use on host=%v by dyno=%v", dynoPtr.Port, host, existingDyno.Container)
				}
			}
		}
		if state == DYNO_STATE_STOPPED {
			// Remove it.
			delete(nodeState.Dynos, container)
		} else {
			// Determine if dyno is already known.
			dyno, ok := nodeState.Dynos[container]
			if ok {
				dynoPtr = dyno
				dynoPtr.State = state // Set updated state.
			} else {
				nodeState.Dynos[container] = dynoPtr
			}
		}
		nodeState.Ts = time.Now()
		//fmt.Printf("map=%v/%v\n", nodeState.Dynos[container], len(nodeState.Dynos))
		//fmt.Printf("map=%v\n", nodeState.Dynos[container])
		return nil
	})
}

func (this *SystemDynoState) ProcessFreeMemoryUpdate(host string, freeMemoryMb int) {
	this.AutoLockHostFn(host, func(host string) error {
		nodeState := this.getOrInitNodeState(host)
		nodeState.FreeMemoryMb = freeMemoryMb
		nodeState.Ts = time.Now()
		return nil
	})
}

func (this *SystemDynoState) GetRunningDynos(application, processType string) []*Dyno {
	this.NodeStatesLock.Lock()
	defer this.NodeStatesLock.Unlock()
	dynos := []*Dyno{}
	for _, nodeState := range this.NodeStates {
		for _, dyno := range nodeState.Dynos {
			if dyno.State == DYNO_STATE_RUNNING && dyno.Application == application && dyno.Process == processType {
				dynos = append(dynos, dyno)
			}
		}
	}
	return dynos
}

func (this *SystemDynoState) GetHostState(host string) *NodeStatus {
	var nodeState *NodeStatus
	this.AutoLockHostFn(host, func(host string) error {
		if newNodeState, ok := this.NodeStates[host]; ok {
			nodeState = &newNodeState
		}
		return nil
	})
	return nil
}

// Attempt to lookup a dyno with a certain state by host/port.
//
// @matchstates Pass `nil` to match any state, othrwise pass a list.
//
// Returns nil if no dyno matching the given match criteria is found.
func (this *SystemDynoState) LookupDynoByHostPort(host string, port int, matchStates ...string) *Dyno {
	var result *Dyno
	this.AutoLockHostFn(host, func(host string) error {
		if nodeState, ok := this.NodeStates[host]; ok {
			for _, dyno := range nodeState.Dynos {
				if dyno.PortNumber == port {
					if matchStates != nil {
						for _, matchState := range matchStates {
							if dyno.State == matchState {
								result = dyno
								return nil
							}
						}
					} else {
						result = dyno
						return nil
					}
				}
			}
		}
		return nil
	})
	return result
}

// // Check if a port is already in use.
// func (this *DynoPortTracker) AlreadyInUse(host string, port int) bool {
// 	this.lock.Lock()
// 	defer this.lock.Unlock()
// 	if ports, ok := this.allocations[host]; ok {
// 		for _, p := range ports {
// 			if p == port {
// 				return true
// 			}
// 		}
// 	}
// 	return false
// }

// // Attempt to allocate a port for a node host.
// func (this *DynoPortTracker) Allocate(host string, port int) error {
// 	this.lock.Lock()
// 	defer this.lock.Unlock()
// 	if ports, ok := this.allocations[host]; ok {
// 		// Require that the port not be already in use.
// 		for _, p := range ports {
// 			if p == port {
// 				return fmt.Errorf("Host/port combination %v/%v is already in use", host, port)
// 			}
// 		}
// 		this.allocations[host] = append(ports, port)
// 	} else {
// 		this.allocations[host] = []int{port}
// 	}
// 	// Schedule the port to be automatically freed once the status monitor will have picked up the in-use port.
// 	go func(host string, port int) {
// 		time.Sleep(1200 * time.Second)
// 		this.Release(host, port)
// 	}(host, port)
// 	fmt.Printf("DynoPortTracker.Allocate: added host=%v port=%v\n", host, port)
// 	return nil
// }

// // Release a previously allocated host/port pair if it is still in the allocations table.
// func (this *DynoPortTracker) Release(host string, port int) {
// 	fmt.Printf("DynoPortTracker.Release: releasing port %v from host %v\n", port, host)
// 	this.lock.Lock()
// 	defer this.lock.Unlock()
// 	if ports, ok := this.allocations[host]; ok {
// 		newPorts := []int{}
// 		for _, p := range ports {
// 			if p != port {
// 				newPorts = append(newPorts, p)
// 			}
// 		}
// 		this.allocations[host] = newPorts
// 	}
// }

// Determine which host to allocate the next dyno on.
func (this *Server) NewDynoGenerator(nodes []*Node, application string, version string) (*DynoGenerator, error) {
	var result *DynoGenerator
	err := this.SystemDynoState.AutoLockFn(func() error {
		// Produce sorted sequence of NodeStatuses.
		allStatuses := []NodeStatusRunning{}
		for _, node := range nodes {
			running := false
			if nodeState, ok := this.SystemDynoState.NodeStates[strings.ToLower(node.Host)]; ok {
				// Determine if there is an identical app/version container already running or allocated on the node.
				for _, dyno := range nodeState.Dynos {
					if dyno.State == DYNO_STATE_RUNNING && dyno.Application == application && dyno.Version == version {
						running = true
						break
					}
				}
				allStatuses = append(allStatuses, NodeStatusRunning{nodeState, running})
			}
		}

		if len(allStatuses) == 0 {
			return fmt.Errorf("The node list was empty, add one or more nodes to enable dyno generation")
		}

		sort.Sort(NodeStatuses(allStatuses))

		result = &DynoGenerator{
			server:      this,
			statuses:    allStatuses,
			position:    0,
			application: application,
			version:     version,
		}

		return nil
	})
	return result, err
}

func (this *NodeStatus) getUsedPorts() []int {
	used := []int{}
	for _, dyno := range this.Dynos {
		used = append(used, dyno.PortNumber)
	}
	sort.Ints(used)
	return used
}

// Not thread-safe (invoker is responsible for locking appropriately).
func (this *SystemDynoState) getNextPort(host string) (int, error) {
	port := -1
	nodeState, ok := this.NodeStates[host]
	if !ok {
		return port, fmt.Errorf("unrecognized host=%v", host)
	}
	usedPorts := nodeState.getUsedPorts()
	port = DYNO_PORT_ALLOCATION_START
	for _, usedPort := range usedPorts {
		if port == usedPort || this.LookupDynoByHostPort(host, port, DYNO_STATE_RUNNING, DYNO_STATE_ALLOCATED) != nil {
			port++
		} else if usedPort > port {
			break
		}
	}
	if port > DYNO_PORT_ALLOCATION_END {
		return port, fmt.Errorf("next dyno port exceeds allocation boundary, allowed range is %v-%v", DYNO_PORT_ALLOCATION_START, DYNO_PORT_ALLOCATION_END)
	}
	return port, nil
}

// Gets the next port to allocate and allocates a dyno for the provided DynoGenerator on the specified node host.
func (this *SystemDynoState) AllocateNextDyno(nodeState *NodeStatus, dynoGenerator *DynoGenerator, process string) (*Dyno, error) {
	var dyno *Dyno
	log.Printf("SystemDynoState.AllocateNextDyno: About to attempt acquisition of SystemDynoState lock\n")
	err := this.AutoLockHostFn(nodeState.Host, func(host string) error {
		log.Printf("SystemDynoState.AllocateNextDyno: SystemDynoState Lock acquired!\n")
		nodeState := this.getOrInitNodeState(host) // Get a fresh locked copy.
		portNumber, err := this.getNextPort(nodeState.Host)
		if err != nil {
			return err
		}
		port := fmt.Sprint(portNumber)
		container := dynoGenerator.application + DYNO_DELIMITER + dynoGenerator.version + DYNO_DELIMITER + process + DYNO_DELIMITER + port
		dyno = ContainerToDyno(nodeState.Host, container, DYNO_STATE_ALLOCATED)
		log.Printf("SystemDynoState.AllocateNextDyno: Allocated new container=%v\n", dyno.Info())
		return nil
	})
	return dyno, err
}

// A
func (this *DynoGenerator) Next(process string) *Dyno {
	nodeState := this.statuses[this.position%len(this.statuses)].status
	this.position++
	dyno, err := this.server.SystemDynoState.AllocateNextDyno(&nodeState, this, process)
	if err != nil {
		log.Printf("DynoGenerator.Next: FATAL ERROR %v\n", err)
		panic(err)
	}
	return dyno
}

// NodeStatus sorting.
func (this NodeStatuses) Len() int { return len(this) } // boilerplate.

// NodeStatus sorting.
func (this NodeStatuses) Swap(i int, j int) { this[i], this[j] = this[j], this[i] } // boilerplate.

// NodeStatus sorting.
func (this NodeStatuses) Less(i int, j int) bool { // actual sorting logic.
	if this[i].running && !this[j].running {
		return true
	}
	if !this[i].running && this[j].running {
		return false
	}
	return this[i].status.FreeMemoryMb > this[j].status.FreeMemoryMb
}

func AppendIfMissing(slice []int, i int) []int {
	for _, ele := range slice {
		if ele == i {
			return slice
		}
	}
	return append(slice, i)
}

// // Get the next available port for a node.
// func (this *Server) getNextPort(nodeState *NodeStatus, usedPorts *[]int) int {
// 	port := DYNO_PORT_ALLOCATION_START
// 	for _, dyno := range nodeState.Dynos {
// 		if dyno.State == DYNO_STATE_RUNNING && dyno.PortNumber > 0 {
// 			*usedPorts = AppendIfMissing(*usedPorts, dyno.PortNumber)
// 		}
// 	}
// 	sort.Ints(*usedPorts)
// 	log.Printf("Server.getNextPort :: Found used ports=%v for host=%v\n", *usedPorts, nodeState.Host)
// 	for _, usedPort := range *usedPorts {
// 		if port == usedPort || dynoPortTracker.AlreadyInUse(nodeState.Host, port) {
// 			port++
// 		} else if usedPort > port {
// 			break
// 		}
// 	}
// 	err := dynoPortTracker.Allocate(nodeState.Host, port)
// 	if err != nil {
// 		log.Printf("Server.getNextPort :: host/port combination %v/%v already in use, will find another\n", nodeState.Host, port)
// 		*usedPorts = AppendIfMissing(*usedPorts, port)
// 		return this.getNextPort(nodeState, usedPorts)
// 	}
// 	log.Printf("Server.getNextPort :: Result port: %v\n", port)
// 	*usedPorts = AppendIfMissing(*usedPorts, port)
// 	return port
// }
