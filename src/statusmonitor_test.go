package main

import (
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
		`echo "'test-app_v1_web_10023' changed state to [STARTING]" ; sleep 1 ;
echo "'test-app_v1_web_10023' changed state to [RUNNING]" ; sleep 1 ;
echo "'test-app_v1_web_10023' changed state to [STOPPING]" ; sleep 1 ;
echo "'test-app_v1_web_10023' changed state to [STOPPED]" ; sleep 1 ; `,
	)
	return cmd
}

//func (this *TestDynoStateChangeListener) Attach() {

//}

func Test_StatusMonitor(t *testing.T) {
	//listener := NodeDynoStateChangeListener{"testlab-sb.threatstream.com"}
	listener := TestDynoStateChangeListener{}
	ch := make(chan string)
	go AttachDynoStateChangeListener(listener, ch)
	time.Sleep(10000000000)
}

func Test_NewDynoState(t *testing.T) {
	type DynoStateTest struct {
		Input          string
		ExpectedResult DynoState
		ExpectedError  error
	}

	tests := []DynoStateTest{
		DynoStateTest{
			Input: `'test-app_v1_web_10023' changed state to [STARTING]`,
			ExpectedResult: DynoState{
				Dyno:  ContainerToDyno("test-app_v1_web_10023"),
				State: "STARTING",
			},
			ExpectedError: nil,
		},
	}

	inputs := []string{
		`'test-app_v1_web_10023' changed state to [STARTING]`,
	}
	for _, input := range inputs {
		_, err := NewDynoState(input)
		if err != nil {
			t.Errorf(`got unexpcted error with input "%v"`, input)
		}
	}
}
