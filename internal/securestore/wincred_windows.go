//go:build windows

package securestore

import (
	"syscall"
	"unsafe"
)

// WinCred stores items as generic credentials in the Windows Credential
// Manager (advapi32 CredWriteW, CredReadW, CredDeleteW). The credential is
// per user and persists on the local machine.
// Built and vetted for Windows; not yet run on a Windows host.
type WinCred struct{ Service string }

var (
	advapi32       = syscall.NewLazyDLL("advapi32.dll")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredRead   = advapi32.NewProc("CredReadW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	errorNotFound           = syscall.Errno(1168)
)

// credentialW mirrors CREDENTIALW.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// NewWinCred returns a Credential Manager store for one service name.
func NewWinCred(service string) *WinCred { return &WinCred{Service: service} }

// Kind names the backend.
func (w *WinCred) Kind() string { return "windows-credential-manager" }

// Available reports whether advapi32 loads.
func (w *WinCred) Available() bool { return advapi32.Load() == nil }

func (w *WinCred) target(item string) (*uint16, error) {
	if err := checkName("service", w.Service); err != nil {
		return nil, err
	}
	if err := checkName("item", item); err != nil {
		return nil, err
	}
	return syscall.UTF16PtrFromString(w.Service + ":" + item)
}

func (w *WinCred) Set(item string, secret []byte) error {
	tn, err := w.target(item)
	if err != nil {
		return err
	}
	un, _ := syscall.UTF16PtrFromString(item)
	c := credentialW{Type: credTypeGeneric, TargetName: tn, CredentialBlobSize: uint32(len(secret)),
		Persist: credPersistLocalMachine, UserName: un}
	if len(secret) > 0 {
		c.CredentialBlob = &secret[0]
	}
	if r, _, e := procCredWrite.Call(uintptr(unsafe.Pointer(&c)), 0); r == 0 {
		return e
	}
	return nil
}

func (w *WinCred) Get(item string) ([]byte, error) {
	tn, err := w.target(item)
	if err != nil {
		return nil, err
	}
	var pc *credentialW
	if r, _, e := procCredRead.Call(uintptr(unsafe.Pointer(tn)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&pc))); r == 0 {
		if e == errorNotFound {
			return nil, ErrNotFound
		}
		return nil, e
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(pc)))
	if pc.CredentialBlobSize == 0 {
		return []byte{}, nil
	}
	return append([]byte(nil), unsafe.Slice(pc.CredentialBlob, pc.CredentialBlobSize)...), nil
}

func (w *WinCred) Delete(item string) error {
	tn, err := w.target(item)
	if err != nil {
		return err
	}
	if r, _, e := procCredDelete.Call(uintptr(unsafe.Pointer(tn)), credTypeGeneric, 0); r == 0 {
		if e == errorNotFound {
			return ErrNotFound
		}
		return e
	}
	return nil
}

func platformStore(service string) (Store, bool) {
	w := NewWinCred(service)
	return w, w.Available()
}
