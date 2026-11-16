package session

// BindingDisposition 仅描述 Session 锁内观察到的权限，不替代 Connection 首终止原因。
type BindingDisposition uint8

const (
	BindingInactive BindingDisposition = iota
	BindingCurrent
	BindingSuperseded
)

func (s *Session) dispositionLocked(b *Binding) BindingDisposition {
	if b == nil || b.session != s {
		return BindingInactive
	}
	if b.superseded {
		return BindingSuperseded
	}
	if s.binding == b && b.active && s.generation == b.Generation && s.status == "CONNECTED" {
		return BindingCurrent
	}
	return BindingInactive
}

func (s *Session) BindingDisposition(b *Binding) BindingDisposition {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dispositionLocked(b)
}

// WithSendAdmission 的调用方必须已持 Connection.mu。检查、选择并标记唯一
// 发送项始终在同一 Session.mu 临界区；回调只允许有界内存操作和 Buffer.Read。
// 禁止网络、阻塞等待、Terminate/Detach、反向取 Connection/登录/操作门锁。
func (s *Session) WithSendAdmission(b *Binding, selectOne func()) BindingDisposition {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.dispositionLocked(b)
	if state == BindingCurrent {
		selectOne()
	}
	return state
}
