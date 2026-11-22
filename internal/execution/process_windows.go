package execution

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func StartProcess(cmd *exec.Cmd) (func() error, func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	// NT 6.1 cannot nest jobs. Only request an OS-authorized breakaway from
	// the launcher's job, then attach our own job before resuming any code.
	version := windows.RtlGetVersion()
	if version.MajorVersion < 6 || (version.MajorVersion == 6 && version.MinorVersion < 2) {
		var inJob uint32
		ok, _, checkErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(
			uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob)))
		if ok == 0 {
			windows.CloseHandle(job)
			return nil, nil, fmt.Errorf("check task job membership: %w", checkErr)
		}
		if inJob != 0 {
			cmd.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
		}
	}
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, nil, err
	}
	fail := func(err error) (func() error, func(), error) {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return nil, nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	windows.CloseHandle(process)
	if err != nil {
		return fail(err)
	}
	// Assign the suspended process before any command code can create descendants.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fail(err)
	}
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	found := false
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == uint32(cmd.Process.Pid) {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				err = openErr
				break
			}
			_, err = windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			found = err == nil
			break
		}
	}
	windows.CloseHandle(snapshot)
	if !found {
		return fail(fmt.Errorf("resume task thread: %v", err))
	}
	kill := func() error { return windows.TerminateJobObject(job, 1) }
	return kill, func() { windows.CloseHandle(job) }, nil
}
