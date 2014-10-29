package main

import (
	"fmt"
	//"io"
	//"log"
	"os/exec"
	"regexp"
	//"strconv"
	//"strings"
	"time"

	"github.com/jaytaylor/streamon"
)

type (
	// `Ts`: No need to specify, will be automatically filled by `.ParseStatus()`.
	NodeStatus struct {
		Host         string
		FreeMemoryMb int
		Containers   []string
		DeployMarker int
		Ts           time.Time
		Err          error
	}
	NodeStatusRequest struct {
		host          string
		resultChannel chan NodeStatus
	}
)

const (
	LXC_STATE_MONITOR_COMMAND = `test $(pgrep --exact lxc-ls | wc -l) -eq 0 && ( sudo lxc-ls --fancy | sed 's/ \{1,\}/ /g' | cut -d' ' -f1,2 | sed "s/\(.*\) \(.*\)/'\1' changed state to [\2]/" && sudo lxc-monitor '.*' ) || exit 1`

	STATUS_MONITOR_CHECK_COMMAND = `echo $(free -m | sed '1,2d' | head -n1 | grep --only '[0-9]\+$') $(sudo lxc-ls --fancy | sed 's/[ \t]\{1,\}/ /g' | grep '^[^_]\+_v[0-9]\+_[^_]\+_[^_]\+ [^ ]\+' | cut -d' ' -f1,2 | tr ' ' '_' | tr '\n' ' ')`
)

var (
	nodeStatusRequestChannel = make(chan NodeStatusRequest)

	dynoStateParserRe = regexp.MustCompile(`'([^']+) changed state to \[([^\]]+)\]'`)
)

/*func (this *NodeStatus) ParseStatus(input string, err error) {
	if err != nil {
		this.Err = err
		return
	}

	tokens := strings.Fields(strings.TrimSpace(input))
	if len(tokens) == 0 {
		this.Err = fmt.Errorf("Parse failed for input '%v'", input)
		return
	}

	this.FreeMemoryMb, err = strconv.Atoi(tokens[0])
	if err != nil {
		this.Err = fmt.Errorf("Integer conversion failed for token '%v' (tokens=%v)", tokens[0], tokens)
		return
	}

	this.Containers = tokens[1:]
	this.Ts = time.Now()
}*/

func RemoteCommand(sshHost string, sshArgs ...string) (string, error) {
	frontArgs := append([]string{"1m", "ssh", DEFAULT_NODE_USERNAME + "@" + sshHost}, defaultSshParametersList...)
	combinedArgs := append(frontArgs, sshArgs...)

	//fmt.Printf("debug: cmd is -> ssh %v <-\n", combinedArgs)
	bs, err := exec.Command("timeout", combinedArgs...).CombinedOutput()

	if err != nil {
		return "", err
	}

	return string(bs), nil
}

/*func checkServer(sshHost string, currentDeployMarker int, ch chan NodeStatus) {
	// Shell command which combines free MB with list of running containers.
	done := make(chan NodeStatus)

	go func() {
		result := NodeStatus{
			Host:         sshHost,
			FreeMemoryMb: -1,
			Containers:   nil,
			DeployMarker: currentDeployMarker,
			Err:          nil,
		}
		result.ParseStatus(RemoteCommand(sshHost, STATUS_MONITOR_CHECK_COMMAND))
		done <- result
	}()

	select {
	case result := <-done: // Captures completed status update.
		ch <- result // Sends result to channel.
	case <-time.After(30 * time.Second):
		ch <- NodeStatus{
			Host:         sshHost,
			FreeMemoryMb: -1,
			Containers:   nil,
			DeployMarker: currentDeployMarker,
			Err:          fmt.Errorf("Timed out for host %v", sshHost),
		} // Sends timeout result to channel.
	}
}*/

func (this *Server) checkNodes(resultChan chan NodeStatus) error {
	// TODO: RESTORE
	// cfg, err := this.getConfig(true)
	// if err != nil {
	// 	return err
	// }
	// currentDeployMarker := deployLock.value()

	// for _, node := range cfg.Nodes {
	// 	go checkServer(node.Host, currentDeployMarker, resultChan)
	// }
	return nil
}

func (this *Server) monitorNodes() {
	repeater := time.Tick(STATUS_MONITOR_INTERVAL_SECONDS * time.Second)
	nodeStatusChan := make(chan NodeStatus)
	hostStatusMap := map[string]NodeStatus{}

	// Kick off the initial checks so we don't have to wait for the next tick.
	this.checkNodes(nodeStatusChan)

	for {
		select {
		case <-repeater:
			this.checkNodes(nodeStatusChan)

		case result := <-nodeStatusChan:
			if deployLock.validateLatest(result.DeployMarker) {
				hostStatusMap[result.Host] = result
				this.pruneDynos(result, &hostStatusMap)
			}

		case request := <-nodeStatusRequestChannel:
			status, ok := hostStatusMap[request.host]
			if !ok {
				status = NodeStatus{
					Host:         request.host,
					FreeMemoryMb: -1,
					Containers:   nil,
					DeployMarker: -1,
					Err:          fmt.Errorf("Unknown host %v", request.host),
				}
			}
			request.resultChannel <- status
		}
	}
}

func (this *Server) getNodeStatus(node *Node) NodeStatus {
	request := NodeStatusRequest{node.Host, make(chan NodeStatus)}
	nodeStatusRequestChannel <- request
	status := <-request.resultChannel
	//fmt.Printf("boom! %v\n", status)
	return status
}

func (this *Server) processDynoStateUpdate(nodeHost, container, state string) {
	/*if _, ok := nodeDynoState[nodeHost]; !ok {
		nodeDynoState[nodeHost] = map[string]string
	}
	nodeState, _ := nodeDynoState[nodeHost]*/
}

// Continually loops the monitoring upon interruption until the a quit channel message is received.
func (this *Server) monitorNodeContainers(nodeHost string, quit chan bool) {
	frontArgs := append([]string{"ssh", DEFAULT_NODE_USERNAME + "@" + nodeHost}, defaultPersistentSshParametersList...)
	commandArgs := append(frontArgs, LXC_STATE_MONITOR_COMMAND)

	for {
		commandListener, err := streamon.NewCommandListener(commandArgs, dynoStateParserRe)
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
				this.processDynoStateUpdate(nodeHost, match[1], match[2])

			case <-quit:
				return
			}
		}
	}
}

/*type (
	DynoStateChangeListener interface {
		GetAttachCommand() *exec.Cmd
		//getChannel() chan string
		//chan string
	}

	NodeDynoStateChangeListener struct {
		host string
		//ch   chan string
	}

	DynoState struct {
		Dyno Dyno
		State string
	}

	DynoStateChangeProcessor struct {
		ch      chan string
		running bool
	}
)

func NewDynoState(host, message string) (*DynoState, error) {
	parsed := dynoStateParserRe.FindStringSubmatch(strings.TrimSpace(message))
	state := parsed[]
	dyno, err := ContainerToDyno(host, container)
	if err != nil {
		return nil, err
	}
	dynoState := DynoState{
		Dyno: dyno,
		State: state,
	}
	return dynoState, nil
}

func (this NodeDynoStateChangeListener) GetAttachCommand() *exec.Cmd {
	sshArgs := append(defaultPersistentSshParametersList, DEFAULT_NODE_USERNAME+"@"+sshHost, "sudo", "lxc-monitor", ".*")
	cmd := exec.Command("ssh", sshArgs...)
	return cmd
}*/
