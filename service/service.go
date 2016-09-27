// +build windows
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/gorilla/mux"
	"github.com/tylerb/graceful"
	"github.com/urfave/negroni"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
)

var elog debug.Log

type svccontext struct {
	conf string
	busy int32 // 0 = idle; 1 = busy
}

// Get the full name (with path) of the executing module.
func getModuleFileName() (string, error) {
	var sysproc = syscall.MustLoadDLL("kernel32.dll").MustFindProc("GetModuleFileNameW")
	b := make([]uint16, syscall.MAX_PATH)
	r, _, err := sysproc.Call(0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	n := uint32(r)
	if n == 0 {
		return "", err
	}

	return string(utf16.Decode(b[0:n])), nil
}

func setUpdateSelfAfterReboot(old string, new string) error {
	var MOVEFILE_DELAY_UNTIL_REBOOT = 0x4
	var sysproc = syscall.MustLoadDLL("kernel32.dll").MustFindProc("MoveFileExW")
	o, err := syscall.UTF16PtrFromString(old)
	if err != nil {
		trace(err.Error())
	}

	n, err := syscall.UTF16PtrFromString(new)
	if err != nil {
		trace(err.Error())
	}

	_, _, _ = sysproc.Call(uintptr(unsafe.Pointer(o)), 0, uintptr(MOVEFILE_DELAY_UNTIL_REBOOT))
	_, _, _ = sysproc.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(o)), uintptr(MOVEFILE_DELAY_UNTIL_REBOOT))
	_, _, _ = sysproc.Call(uintptr(unsafe.Pointer(n)), 0, uintptr(MOVEFILE_DELAY_UNTIL_REBOOT))

	return nil
}

func rebootSystem() error {
	c := exec.Command("shutdown", "/r", "/t", "10")
	if err := c.Run(); err != nil {
		trace(err.Error())
		return err
	}

	return nil
}

func updateSelf(m *svccontext) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// By default, we reboot after setup update. To cancel, we need reboot=false param.
		reboot := true
		rb, ok := q["reboot"]
		if ok {
			if rb[0] == "false" {
				reboot = false
			}
		}

		r.ParseMultipartForm(32 << 20)
		file, handler, err := r.FormFile("uploadfile")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		defer file.Close()
		str := fmt.Sprintf("Handler.Header: %v", handler.Header)
		trace(str)
		path, _ := getModuleFileName()
		_, fstr := filepath.Split(handler.Filename)
		fstr = filepath.Dir(path) + `\` + fstr + `_new`
		f, err := os.Create(fstr)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		defer f.Close()
		io.Copy(f, file)
		fmt.Fprintf(w, "Self update applied after reboot.")
		trace(path + ` --> ` + fstr)
		err = setUpdateSelfAfterReboot(path, fstr)
		if reboot {
			trace("Rebooting system...")
			rebootSystem()
		}
	})
}

func getInternalVersion(m *svccontext) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, internalVersion)
	})
}

func (m *svccontext) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPauseAndContinue
	changes <- svc.Status{State: svc.StartPending}
	//tickdef := 86400 * time.Second
	tickdef := 10 * time.Second

	// Start our main http interface
	go func() {
		mux := mux.NewRouter()
		// API version 1
		v1 := mux.PathPrefix("/api/v1").Subrouter()
		v1.Methods("GET").Path("/version").Handler(getInternalVersion(m))
		v1.Methods("POST").Path("/update/self").Handler(updateSelf(m))
		n := negroni.Classic()
		n.UseHandler(mux)
		trace("Launching http interface...")
		graceful.Run(":8080", 5*time.Second, n)
	}()

	maintick := time.Tick(tickdef)
	slowtick := time.Tick(2 * time.Second)
	tick := maintick
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
loop:
	for {
		select {
		case <-tick:
			trace("timer tick")
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
				// Testing deadlock from https://code.google.com/p/winsvc/issues/detail?id=4
				time.Sleep(100 * time.Millisecond)
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				break loop
			case svc.Pause:
				changes <- svc.Status{State: svc.Paused, Accepts: cmdsAccepted}
				tick = slowtick
			case svc.Continue:
				changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
				tick = maintick
			default:
				elog.Error(1, fmt.Sprintf("unexpected control request #%d", c))
			}
		}
	}
	changes <- svc.Status{State: svc.StopPending}
	return
}

func runService(name string, conf string, isDebug bool) {
	var err error
	if isDebug {
		elog = debug.New(name)
	} else {
		elog, err = eventlog.Open(name)
		if err != nil {
			return
		}
	}
	defer elog.Close()

	elog.Info(1, fmt.Sprintf("starting %s service", name))
	run := svc.Run
	if isDebug {
		run = debug.Run
	}
	err = run(name, &svccontext{conf: conf, busy: 0})
	if err != nil {
		elog.Error(1, fmt.Sprintf("%s service failed: %v", name, err))
		return
	}
	elog.Info(1, fmt.Sprintf("%s service stopped", name))
}
