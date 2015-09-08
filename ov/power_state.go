package ov

import (
	"encoding/json"
	"time"
	"strings"
	"errors"

	"github.com/docker/machine/log"
	"github.com/docker/machine/drivers/oneview/rest"
)
// Create a PowerState type
type PowerState int

type Power struct {
	Blade *ServerHardware
  State PowerState
  TaskStatus bool
}

const (
	P_ON    PowerState = 1 + iota
	P_OFF
	P_UKNOWN
)

var powerstates = [...]string {
	"On",
	"Off",
	"UNKNOWN",
}

func (p PowerState) String() string { return powerstates[p-1] }
func (p PowerState) Equal(s string) (bool) {return (strings.ToUpper(s) == strings.ToUpper(p.String()))}

// Power control
type PowerControl int

const(
	P_COLDBOOT   PowerControl = 1 + iota
	P_MOMPRESS
	P_RESET
)

var powercontrols = [...]string {
	"ColdBoot", 			// ColdBoot       - A hard reset that immediately removes power from the server
										//                hardware and then restarts the server after approximately six seconds.
	"MomentaryPress", // MomentaryPress - Power on or a normal (soft) power off,
										//                  depending on powerState. PressAndHold
										//                  An immediate (hard) shutdown.
	"Reset",					// Reset          - A normal server reset that resets the device in an orderly sequence.
}

func (pc PowerControl) String() string { return powercontrols[pc-1] }

// Provides power execution status
type PowerTask struct {
	Blade       *ServerHardware
  State       PowerState       // current power state
  TaskStatus  bool             // when true, task are done
	CurrentTask *Task            // the uri to the task that has been submitted
	Timeout     int              // time before timeout on Executor
	WaitTime    time.Duration    // time between task checks
}

// Create a new power task manager
func ( pt *PowerTask ) NewPowerTask( b ServerHardware)(*PowerTask) {
	return &PowerTask{Blade:       &b,
										State:       P_UKNOWN,
										TaskStatus:  false,
										CurrentTask: &Task {  URI: "", Name: "", Owner: ""},
										Timeout:     36, // default 6min
										WaitTime:    10} // default 10sec, impacts Timeout
}

// reset the power task back to off
func ( pt *PowerTask) ResetTask() {
	pt.State       = P_UKNOWN
	pt.TaskStatus  = false
	pt.CurrentTask = &Task{ URI: "", Name: "", Owner: ""}
}

// get current power state
func ( pt *PowerTask) GetCurrentPowerState()(error) {
	// Quick check to make sure we have a proper hardware blade
	if pt.Blade.URI == "" {pt.State = P_UKNOWN; return errors.New("Can't get power on blade without hardware") }

	// get the latest state based on current blade uri
	b, err := pt.Blade.Client.GetServerHardware(pt.Blade.URI)
	if err != nil { return err}
  log.Debugf("GetCurrentPowerState() blade -> %+v",b)
	// Set the current state of the blade as a constant
	if P_OFF.Equal(b.PowerState) {
		pt.State = P_OFF
	} else if P_ON.Equal(b.PowerState) {
		pt.State = P_ON
	} else {
		log.Warnf("Un-known power state detected %s, for %s.", b.PowerState, b.Name)
		pt.State = P_UKNOWN
	}
  // Reassign the current blade and state of that blade
	pt.Blade = &b
	return nil
}

// PowerRequest
// { 'body' => { 'powerState' => state.capitalize, 'powerControl' => 'MomentaryPress' } })
type PowerRequest struct {
	PowerState    string `json:"powerState,omitempty"`
	PowerControl  string `json:"powerControl,omitempty"`
}

// Submit desired power state
func ( pt *PowerTask) SubmitPowerState(s PowerState) {
	if err := pt.GetCurrentPowerState(); err != nil {
		log.Errorf("Error getting current power state: %s", err)
		return
	}
	if s != pt.State {
	  log.Infof("Powering %s server %s for %s.",s,pt.Blade.Name, pt.Blade.SerialNumber)
		var (
			body = PowerRequest{PowerState: s.String(), PowerControl: P_MOMPRESS.String()}
			uri  = strings.Join([]string{	pt.Blade.URI,
																		"/powerState" },"")
		)
		log.Debugf("REST : %s \n %+v\n", uri, body)
		log.Debugf("pt -> %+v", pt)
		data, err := pt.Blade.Client.RestAPICall(rest.PUT, uri , body)
		if err != nil {
			pt.TaskStatus = true
			log.Errorf("Error with power state request: %s", err)
			return
		 }

		log.Debugf("SubmitPowerState %s", data)
		if err := json.Unmarshal([]byte(data), &pt.CurrentTask); err != nil {
			pt.TaskStatus = true
			log.Errorf("Error with power state un-marshal: %s", err)
			return
		}
	} else {
		log.Infof("Desired Power State already set -> %s", pt.State)
		pt.TaskStatus = true
	}

	return
}

// Get current task status
func ( pt *PowerTask) GetCurrentTaskStatus()(error) {
	log.Debugf("Working on getting current blade task status")
	var (
		uri  = pt.CurrentTask.URI
	)
	if uri != "" {
		data, err := pt.Blade.Client.RestAPICall(rest.GET, uri, nil)
		if err != nil {
			return err
		}
		log.Debugf("data: %s",data)
		if err := json.Unmarshal([]byte(data), &pt.CurrentTask); err != nil {
			return err
		}
	} else {
		log.Debugf("Unable to get current task, no URI found")
	}
	return nil
}

// Submit desired power state and wait
// Most of our concurrency will happen in PowerExecutor
func ( pt *PowerTask) PowerExecutor(s PowerState)(error) {
	currenttime := 0
	pt.ResetTask()
	go pt.SubmitPowerState(s)
	for !pt.TaskStatus && (currenttime < pt.Timeout) {
		if err := pt.GetCurrentTaskStatus(); err != nil {
			return err
		}
		if pt.CurrentTask.URI != "" && T_COMPLETED.Equal(pt.CurrentTask.TaskState) {
			pt.TaskStatus = true
		}
		if pt.CurrentTask.URI != "" {
			log.Debugf("Waiting to set power state %s for blade %s, %s", s, pt.Blade.Name)
			log.Infof("Working on power state,%d%%, %s.", pt.CurrentTask.ComputedPercentComplete, pt.CurrentTask.TaskStatus)
		} else {
			log.Info("Working on power state.")
		}

		// wait time before next check
		time.Sleep(time.Millisecond * (1000 * pt.WaitTime)) // wait 10sec before checking the status again
		currenttime++
	}
	if !(currenttime < pt.Timeout) {
		log.Warnf("Power %s state timed out for %s.", s, pt.Blade.Name)
	}
	log.Infof("Power Task Execution Completed")
	return nil
}
