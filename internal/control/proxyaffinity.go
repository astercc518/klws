package control

import "context"

// StickyBindProxy 返回一个粘性绑定函数：账号已绑代理则跳过，仅未绑时调 bind。
// getBound 应封装 store.GetBoundProxy（绑定存在=true，ErrProxyNotBound=false）。
func StickyBindProxy(
	getBound func(ctx context.Context, jid string) (bool, error),
	bind func(ctx context.Context, jid, cc string) error,
) func(ctx context.Context, jid, cc string) error {
	return func(ctx context.Context, jid, cc string) error {
		ok, err := getBound(ctx, jid)
		if err == nil && ok { return nil } // 已绑 → 粘性，跳过
		return bind(ctx, jid, cc)
	}
}
