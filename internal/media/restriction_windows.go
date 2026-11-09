package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	securityCapabilitiesAttribute = 0x00020009 // PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES
	tokenIsAppContainer           = 29         // TOKEN_INFORMATION_CLASS
)

type securityCapabilities struct {
	appContainerSID *windows.SID
	capabilities    *windows.SIDAndAttributes
	capabilityCount uint32
	reserved        uint32
}

type codecBundle struct {
	executable string
	dir        string
	sid        *windows.SID
}

type codecSandbox struct {
	bundle func() (codecBundle, error)
}

func newCodecSandbox(executable, scratch string) codecSandbox {
	// The broker serializes launches. Cache successful staging so a
	// temporary disk error can be retried without restarting the editor.
	var staged codecBundle
	return codecSandbox{bundle: func() (codecBundle, error) {
		if staged.executable != "" {
			return staged, nil
		}
		var err error
		staged, err = stageBundle(executable, scratch)
		return staged, err
	}}
}

var codecSID = sync.OnceValues(func() (*windows.SID, error) {
	userenv := windows.NewLazySystemDLL("userenv.dll")
	create := userenv.NewProc("CreateAppContainerProfile")
	derive := userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	name, _ := windows.UTF16PtrFromString("DigiPhoto.Codec")
	title, _ := windows.UTF16PtrFromString("DigiPhoto image codec")
	var sid *windows.SID
	hr, _, _ := create.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(title)),
		uintptr(unsafe.Pointer(title)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if uint32(hr) == 0x800700b7 { // HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS)
		hr, _, _ = derive.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid)))
	}
	if uint32(hr) != 0 || sid == nil {
		return nil, fmt.Errorf("create codec AppContainer identity (HRESULT 0x%08x)", uint32(hr))
	}
	defer windows.FreeSid(sid)
	return sid.Copy()
})

func stageBundle(executable, scratch string) (codecBundle, error) {
	sid, err := codecSID()
	if err != nil {
		return codecBundle{}, err
	}
	dir, err := os.MkdirTemp(scratch, "codec-runtime-")
	if err != nil {
		return codecBundle{}, err
	}
	created := dir
	staged := false
	defer func() {
		if !staged {
			os.RemoveAll(created)
		}
	}()
	dir, err = filepath.Abs(dir)
	if err != nil {
		return codecBundle{}, err
	}
	if err := grantCodecAccess(dir, sid, windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE); err != nil {
		return codecBundle{}, err
	}
	dest := filepath.Join(dir, "DigiPhotoCodec.exe")
	if err := copyCodecBinary(executable, dest, sid); err != nil {
		return codecBundle{}, err
	}
	dll := filepath.Join(filepath.Dir(executable), "digiphoto_codecs.dll")
	if _, err := os.Stat(dll); err == nil {
		if err := copyCodecBinary(dll, filepath.Join(dir, "digiphoto_codecs.dll"), sid); err != nil {
			return codecBundle{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return codecBundle{}, err
	}
	staged = true
	return codecBundle{executable: dest, dir: dir, sid: sid}, nil
}

func copyCodecBinary(src, dest string, sid *windows.SID) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err := errors.Join(err, out.Close()); err != nil {
		return err
	}
	return grantCodecAccess(dest, sid, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE)
}

func grantCodecAccess(path string, sid *windows.SID, access windows.ACCESS_MASK) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: access,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID,
			TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)},
	}}, old)
	runtime.KeepAlive(sid)
	runtime.KeepAlive(sd)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func newCodecJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
				windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY,
			ActiveProcessLimit: 1,
		},
		ProcessMemoryLimit: 768 << 20,
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func (s codecSandbox) run(ctx context.Context, stdio [3]*os.File, args []string) error {
	bundle, err := s.bundle()
	if err != nil {
		return fmt.Errorf("prepare codec sandbox: %w", err)
	}
	job, err := newCodecJob()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	if err := ctx.Err(); err != nil {
		return err
	}
	proc, err := bundle.start(stdio, args)
	stdio[1].Close()
	stdio[2].Close()
	if err != nil {
		return fmt.Errorf("launch codec AppContainer: %w", err)
	}
	defer windows.CloseHandle(proc.Process)
	defer windows.CloseHandle(proc.Thread)
	if err := windows.AssignProcessToJobObject(job, proc.Process); err != nil {
		windows.TerminateProcess(proc.Process, 1)
		windows.WaitForSingleObject(proc.Process, windows.INFINITE)
		return fmt.Errorf("assign codec to its job: %w", err)
	}
	if _, err := windows.ResumeThread(proc.Thread); err != nil {
		return err
	}
	return waitCodec(ctx, job, proc.Process)
}

func (b codecBundle) start(stdio [3]*os.File, args []string) (windows.ProcessInformation, error) {
	var proc windows.ProcessInformation
	var handles [3]windows.Handle
	current := windows.CurrentProcess()
	for i, f := range stdio {
		err := windows.DuplicateHandle(current, windows.Handle(f.Fd()), current, &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS)
		if err != nil {
			return proc, err
		}
		defer windows.CloseHandle(handles[i])
	}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return proc, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return proc, err
	}
	// Grant the package identity zero network or other capabilities.
	caps := securityCapabilities{appContainerSID: b.sid}
	if err := attrs.Update(securityCapabilitiesAttribute, unsafe.Pointer(&caps), unsafe.Sizeof(caps)); err != nil {
		return proc, err
	}
	var startup windows.StartupInfoEx
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[2]
	startup.ProcThreadAttributeList = attrs.List()
	exe, err := windows.UTF16PtrFromString(b.executable)
	if err != nil {
		return proc, err
	}
	dir, err := windows.UTF16PtrFromString(b.dir)
	if err != nil {
		return proc, err
	}
	argv := append([]string{b.executable, "--codec-helper"}, args...)
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return proc, err
	}
	env := utf16.Encode([]rune(strings.Join([]string{
		"GOMEMLIMIT=448MiB", "GOMAXPROCS=1", "LOCALAPPDATA=" + os.Getenv("LOCALAPPDATA"), "SystemRoot=" + os.Getenv("SystemRoot"),
	}, "\x00") + "\x00\x00"))
	err = windows.CreateProcess(exe, line, nil, nil, true,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_SUSPENDED|windows.CREATE_NO_WINDOW,
		&env[0], dir, &startup.StartupInfo, &proc)
	runtime.KeepAlive(caps)
	runtime.KeepAlive(stdio)
	return proc, err
}

func waitCodec(ctx context.Context, job, process windows.Handle) error {
	for {
		if err := ctx.Err(); err != nil {
			if windows.TerminateJobObject(job, 1) != nil {
				windows.TerminateProcess(process, 1)
			}
			windows.WaitForSingleObject(process, 5000)
			return err
		}
		result, err := windows.WaitForSingleObject(process, 50)
		if err != nil {
			return err
		}
		if result == windows.WAIT_OBJECT_0 {
			var code uint32
			if err := windows.GetExitCodeProcess(process, &code); err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("codec process exited with status 0x%08x", code)
			}
			return nil
		}
	}
}

func enterRestriction(args []string) ([]string, error) {
	var isAppContainer, size uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), tokenIsAppContainer,
		(*byte)(unsafe.Pointer(&isAppContainer)), uint32(unsafe.Sizeof(isAppContainer)), &size)
	if err != nil {
		return nil, fmt.Errorf("verify codec AppContainer: %w", err)
	}
	if isAppContainer == 0 {
		return nil, errors.New("codec must run in a Windows AppContainer")
	}
	return args, nil
}
