package management

import (
	"context"
	"debug/pe"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

type memoryStatusEx struct {
	Length, Load                     uint32
	TotalPhys, AvailablePhys         uint64
	TotalPageFile, AvailablePageFile uint64
	TotalVirtual, AvailableVirtual   uint64
	AvailableExtendedVirtual         uint64
}

// 概览只调用系统接口和只读注册表，不启动 Shell、PowerShell 或外部采集程序。
func collectHostInformation(parent context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	timeout := errors.New("HOST_INFO_TIMEOUT")
	if ctx.Err() != nil {
		return nil, timeout
	}
	name, err := Hostname()
	if err != nil || name == "" {
		return nil, errors.New("HOST_INFO_UNAVAILABLE")
	}
	var records strings.Builder
	add := func(kind string, fields ...string) {
		records.WriteString(kind)
		for _, field := range fields {
			// 保持与其他系统相同的字段边界，不把异常注册表值当作完整快照返回。
			if len(field) > 4096 || !utf8.ValidString(field) || strings.ContainsRune(field, 0) {
				field = ""
			}
			records.WriteByte('\t')
			records.WriteString(base64.StdEncoding.EncodeToString([]byte(field)))
		}
		records.WriteByte('\n')
	}
	version := windows.RtlGetVersion()
	osName := windowsProductName(readHostRegistryString(
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "ProductName"), version.BuildNumber, version.ProductType)
	osVersion := fmt.Sprintf("%d.%d.%d", version.MajorVersion, version.MinorVersion, version.BuildNumber)
	kernel := strconv.FormatUint(uint64(version.BuildNumber), 10)
	now := time.Now()
	zone := readHostRegistryString(`SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, "TimeZoneKeyName")
	if zone == "" {
		zone, _ = now.Zone()
	}
	add("system", name, osName, osVersion, kernel, nativeHostArchitecture(), nativeHostAccount(),
		nativeHostPrivilege(), strconv.FormatInt(int64(windows.DurationSinceBoot()/time.Second), 10),
		now.Format(time.RFC3339Nano), zone)
	model := readHostRegistryString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString")
	add("cpu", model, strconv.FormatUint(uint64(windows.GetActiveProcessorCount(windows.ALL_PROCESSOR_GROUPS)), 10))
	memory := memoryStatusEx{}
	memory.Length = uint32(unsafe.Sizeof(memory))
	if ok, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memory))); ok != 0 {
		add("memory", strconv.FormatUint(memory.TotalPhys, 10), strconv.FormatUint(memory.AvailablePhys, 10))
	}
	if ctx.Err() != nil {
		return nil, timeout
	}
	if mask, err := windows.GetLogicalDrives(); err == nil {
		for _, volume := range volumeEntries(mask) {
			if ctx.Err() != nil {
				return nil, timeout
			}
			root, err := windows.UTF16PtrFromString(volume.Path)
			// 不查询网络盘、光驱和可移动介质，避免不可用设备拖延基础概览。
			if err != nil || windows.GetDriveType(root) != windows.DRIVE_FIXED {
				continue
			}
			var available, total, free uint64
			totalText, availableText := "", ""
			if windows.GetDiskFreeSpaceEx(root, &available, &total, &free) == nil {
				totalText, availableText = strconv.FormatUint(total, 10), strconv.FormatUint(available, 10)
			}
			var filesystem [256]uint16
			var serial, componentLength, flags uint32
			fs := ""
			if windows.GetVolumeInformation(root, nil, 0, &serial, &componentLength, &flags, &filesystem[0], uint32(len(filesystem))) == nil {
				fs = windows.UTF16ToString(filesystem[:])
			}
			add("disk", volume.Path, fs, totalText, availableText)
		}
	}
	if ctx.Err() != nil {
		return nil, timeout
	}
	if interfaces, err := net.Interfaces(); err == nil {
		for i, nic := range interfaces {
			if i >= 64 {
				break
			}
			if ctx.Err() != nil {
				return nil, timeout
			}
			state := "down"
			if nic.Flags&net.FlagUp != 0 {
				state = "up"
			}
			addresses := []string{}
			if values, err := nic.Addrs(); err == nil {
				for _, address := range values {
					if text := address.String(); len(text) <= 128 && len(addresses) < 32 {
						addresses = append(addresses, text)
					}
				}
			}
			add("network", nic.Name, nic.HardwareAddr.String(), state, strings.Join(addresses, "\n"))
		}
	}
	// 原生调用由系统返回；过期后不发布迟到的快照，也不另启无法取消的采集 goroutine。
	if ctx.Err() != nil {
		return nil, timeout
	}
	return parseHostInformation([]byte(records.String()))
}

func readHostRegistryString(path, name string) string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func windowsProductName(product string, build uint32, productType byte) string {
	// Windows 11 的 ProductName 仍可能写作 Windows 10；服务器版本不适用此替换。
	if productType == 1 && build >= 22000 && strings.HasPrefix(product, "Windows 10") {
		return "Windows 11" + strings.TrimPrefix(product, "Windows 10")
	}
	if product == "" {
		return "Windows"
	}
	return product
}

func nativeHostArchitecture() string {
	var processMachine, nativeMachine uint16
	if windows.IsWow64Process2(windows.CurrentProcess(), &processMachine, &nativeMachine) == nil {
		switch nativeMachine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			return "amd64"
		case pe.IMAGE_FILE_MACHINE_ARM64:
			return "arm64"
		case pe.IMAGE_FILE_MACHINE_I386:
			return "386"
		}
	}
	return runtime.GOARCH
}

func nativeHostAccount() string {
	var buffer [512]uint16
	length := uint32(len(buffer))
	if windows.GetUserNameEx(windows.NameSamCompatible, &buffer[0], &length) != nil {
		return ""
	}
	return windows.UTF16ToString(buffer[:])
}

func nativeHostPrivilege() string {
	var elevated, size uint32
	if windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &size) != nil {
		return ""
	}
	if elevated != 0 {
		return "administrator"
	}
	return "user"
}
