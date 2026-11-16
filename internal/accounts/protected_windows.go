//go:build windows

package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type protectedStore struct {
	dir  string
	lock windows.Handle
}

func newProtectedStore(root string) (secretStore, error) {
	if root == "" {
		return nil, errStorage
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, errStorage
	}
	dir := filepath.Join(root, "accounts")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, errStorage
	}
	if err = checkPrivatePath(dir); err != nil {
		return nil, errStorage
	}
	if err = privateACL(dir); err != nil {
		return nil, errStorage
	}
	lockPath := filepath.Join(dir, "vault.lock")
	if err = checkPrivatePath(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errStorage
	}
	p, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, errStorage
	}
	handle, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, errStorage
	}
	return &protectedStore{dir: dir, lock: handle}, nil
}
func (s *protectedStore) Close() error { return windows.CloseHandle(s.lock) }

// Do not follow junctions/symlinks at any store-controlled path.
func checkPrivatePath(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errStorage
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errStorage
	}
	return nil
}
func privateACL(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	// Protected DACL: only this Windows user and SYSTEM; children inherit it.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func (s *protectedStore) filename(name string) (string, error) {
	if !secretNamePattern.MatchString(name) {
		return "", errStorage
	}
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".dpapi"), nil
}
func entropyFor(name string) []byte { return []byte("NeoLauncher/protected-accounts/v1/" + name) }
func blob(b []byte) windows.DataBlob {
	if len(b) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}
func crypt(data, entropy []byte, protect bool) ([]byte, error) {
	in, extra := blob(data), blob(entropy)
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, &extra, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, &extra, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if err != nil {
		return nil, errStorage
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Data == nil || out.Size > maxVaultSize+65536 {
		return nil, errStorage
	}
	native := unsafe.Slice(out.Data, int(out.Size))
	result := append([]byte(nil), native...)
	clear(native)
	return result, nil
}
func (s *protectedStore) Get(name string) ([]byte, error) {
	path, err := s.filename(name)
	if err != nil {
		return nil, errStorage
	}
	if checkPrivatePath(s.dir) != nil {
		return nil, errStorage
	}
	if err = checkPrivatePath(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, errStorage
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errStorage
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxVaultSize+65536 {
		return nil, errStorage
	}
	ciphertext, err := io.ReadAll(io.LimitReader(f, maxVaultSize+65537))
	if err != nil || len(ciphertext) > maxVaultSize+65536 {
		return nil, errStorage
	}
	plaintext, err := crypt(ciphertext, entropyFor(name), false)
	if err != nil || len(plaintext) > maxVaultSize {
		clear(plaintext)
		return nil, errStorage
	}
	return plaintext, nil
}
func (s *protectedStore) Set(name string, value []byte) error {
	path, err := s.filename(name)
	if err != nil || len(value) > maxVaultSize {
		return errStorage
	}
	if checkPrivatePath(s.dir) != nil {
		return errStorage
	}
	if err = checkPrivatePath(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errStorage
	}
	ciphertext, err := crypt(value, entropyFor(name), true)
	if err != nil {
		return errStorage
	}
	f, err := os.CreateTemp(s.dir, ".vault-*.tmp")
	if err != nil {
		return errStorage
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	if privateACL(temp) != nil {
		return errStorage
	}
	if _, err = f.Write(ciphertext); err != nil {
		return errStorage
	}
	if f.Sync() != nil {
		return errStorage
	}
	if f.Close() != nil {
		return errStorage
	}
	from, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return errStorage
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errStorage
	}
	if windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH) != nil {
		return errStorage
	}
	return nil
}
