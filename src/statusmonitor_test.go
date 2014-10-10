package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

type (
	TestDynoStateChangeListener struct {
	}
)

func (this TestDynoStateChangeListener) GetAttachCommand() *exec.Cmd {
	cmd := exec.Command(
		"bash",
		"-c",
		`echo "'test' changed state to [STARTING]" ; sleep 1 ;
echo "'test' changed state to [RUNNING]" ; sleep 1 ;
echo "'test' changed state to [STOPPING]" ; sleep 1 ;
echo "'test' changed state to [STOPPED]" ; sleep 1 ; `,
	)
	return cmd
}

//func (this *TestDynoStateChangeListener) Attach() {

//}

func Test_StatusMonitor(t *testing.T) {
	t.Log("hello")

	input := `'test' changed state to [STARTING]
'test' changed state to [RUNNING]
'test' changed state to [STOPPING]
'test' changed state to [STOPPED]
`
	t.Logf("input=%v\n", input)

	//listener := NodeDynoStateChangeListener{"testlab-sb.threatstream.com"}
	listener := TestDynoStateChangeListener{}
	go AttachDynoStateChangeListener(listener, os.Stdout)
	time.Sleep(10000000000)
}
