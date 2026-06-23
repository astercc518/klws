// internal/store/device.go
package store

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

var ErrDeviceNotFound = errors.New("store: device not registered")

// GetDeviceStore 实时从 Postgres 取指定账号的 whatsmeow 凭证。
// 未注册返回 ErrDeviceNotFound。返回的 Device 为该账号专属,不可跨账号复用。
func (m *Manager) GetDeviceStore(ctx context.Context, accountJID string) (*store.Device, error) {
	jid, err := types.ParseJID(accountJID)
	if err != nil {
		return nil, fmt.Errorf("parse jid %q: %w", accountJID, err)
	}
	device, err := m.container.GetDevice(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("get device %s: %w", jid, err)
	}
	if device == nil {
		return nil, ErrDeviceNotFound
	}
	return device, nil
}

// NewDeviceStore 为新账号创建空白 Device,供后续扫码/配对。
func (m *Manager) NewDeviceStore(_ context.Context) *store.Device {
	return m.container.NewDevice()
}
